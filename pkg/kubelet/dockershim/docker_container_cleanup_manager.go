/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/*
This file contains the structure responsible to clean up stale container clean up infos,
i.e. those that have still not been performed more than staleContainerCleanupInfoAge after
the container's creation.
*/

package dockershim

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/util/orderedmap"
)

type containerCleanupManager struct {
	cleanupService containerCleanupService
	// Those are the cleanup infos that haven't been triggered yet.
	pendingCleanupInfos *orderedmap.OrderedMap
	// Those are the cleanup infos that have been triggered, but have failed.
	// We store them here to retry them later.
	// A given container ID can only be present in one of the 2 maps at any one time.
	failedCleanupInfos map[string]*containerCleanupInfoWithFailureCount
	mutex              *sync.Mutex
}

type containerCleanupInfoWithTimestamp struct {
	cleanupInfo *containerCleanupInfo
	timestamp   int64
}

type containerCleanupInfoWithFailureCount struct {
	cleanupInfo  *containerCleanupInfo
	failureCount int
}

// An interface containing the one of dockerService's methods that we need here;
// allows mocking it in tests.
type containerCleanupService interface {
	performContainerCleanup(containerID string, cleanupInfo *containerCleanupInfo) []error
}

func newDockerContainerCleanupManager(cleanupService containerCleanupService) *containerCleanupManager {
	return &containerCleanupManager{
		cleanupService:      cleanupService,
		pendingCleanupInfos: orderedmap.New(),
		failedCleanupInfos:  make(map[string]*containerCleanupInfoWithFailureCount),
		mutex:               &sync.Mutex{},
	}
}

// start makes the manager periodically review the cleanup infos it knows about and clean up the
// ones that become stale.
// This is a blocking call.
// It can be passed up to do channels: the first one will stop the manager when closed;
// The second one will be sent to after each "tick", ie call to unsafeCleanupStaleContainerCleanupInfos,
// giving the IDs of containers that got cleaned up, if any - regardless of whether the cleanup was
// successful.
// These channels are mainly intended for tests, and both can be left nil.
func (cm *containerCleanupManager) start(stopChannel <-chan struct{}, tickChannel chan []string) {
	if stopChannel == nil {
		stopChannel = wait.NeverStop
	}

	wait.Until(func() {
		cm.mutex.Lock()
		defer cm.mutex.Unlock()

		containerIDs := cm.unsafeRetryFailedCleanups()
		containerIDs = append(containerIDs, cm.unsafeCleanupStaleContainerCleanupInfos()...)

		if tickChannel != nil {
			tickChannel <- containerIDs
		}
	}, staleContainerCleanupInterval, stopChannel)
}

// Having those as variables makes them easy to mock in unit tests.
var (
	currentNanoTimestampFunc = func() int64 {
		return time.Now().UnixNano()
	}

	// The period in between 2 calls to cleanupStaleContainers once started.
	staleContainerCleanupInterval = 5 * time.Minute

	// Cleanup infos older than this much will be considered stale, and cleaned up.
	staleContainerCleanupInfoAge = 1 * time.Hour
)

// If failing to clean up a container that many times, we'll just log an error and forget about this container.
const maxFailures = 5

// insert allows keeping track of a new container's cleanup info.
func (cm *containerCleanupManager) insert(containerID string, cleanupInfo *containerCleanupInfo) {
	if cleanupInfo == nil {
		return
	}

	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	if _, present := cm.pendingCleanupInfos.Get(containerID); present {
		// shouldn't happen, as we only insert at container creation time - but to err on the side of
		// caution, let's delete from and re-insert into the ordered map to ensure that the order
		// stays chronological
		klog.Errorf("duplicate cleanup info for container ID %q", containerID)
		cm.pendingCleanupInfos.Delete(containerID)
	}

	cm.pendingCleanupInfos.Set(containerID, &containerCleanupInfoWithTimestamp{
		cleanupInfo: cleanupInfo,
		timestamp:   currentNanoTimestampFunc(),
	})

	if withFailureCount, present := cm.failedCleanupInfos[containerID]; present {
		// this shouldn't happen either for the same reason; but same as above, if
		// it does happen let's log it and maintain a sane internal state
		klog.Errorf("duplicate cleanup info for container ID %q - had already tried to clean it up %v times unsuccessfully",
			containerID, withFailureCount.failureCount)
		delete(cm.failedCleanupInfos, containerID)
	}
}

// performCleanup cleans up the given containerID.
func (cm *containerCleanupManager) performCleanup(containerID string) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	cm.unsafePerformCleanup(containerID)
}

// unsafePerformCleanup is the same as performCleanup, but assumes the manager's lock has already
// been acquired.
func (cm *containerCleanupManager) unsafePerformCleanup(containerID string) {
	if cleanupInfoWithTimestamp, present := cm.pendingCleanupInfos.Delete(containerID); present {
		cleanupInfo := cleanupInfoWithTimestamp.(*containerCleanupInfoWithTimestamp).cleanupInfo

		if errors := cm.cleanupService.performContainerCleanup(containerID, cleanupInfo); len(errors) != 0 {
			cm.failedCleanupInfos[containerID] = &containerCleanupInfoWithFailureCount{
				cleanupInfo:  cleanupInfo,
				failureCount: 1,
			}
		}
	} else {
		cm.unsafeRetryFailedCleanup(containerID)
	}
}

// unsafeRetryFailedCleanup re-tries running a clean up that has already failed earlier.
// It assumes that the manager's lock has already been acquired.
func (cm *containerCleanupManager) unsafeRetryFailedCleanup(containerID string) {
	if cleanupInfoWithFailureCount, present := cm.failedCleanupInfos[containerID]; present {
		if errors := cm.cleanupService.performContainerCleanup(containerID, cleanupInfoWithFailureCount.cleanupInfo); len(errors) == 0 {
			delete(cm.failedCleanupInfos, containerID)
		} else {
			cleanupInfoWithFailureCount.failureCount++
			if cleanupInfoWithFailureCount.failureCount >= maxFailures {
				klog.Errorf("unable to clean up container %q, giving up after %v failures", containerID, cleanupInfoWithFailureCount.failureCount)
				delete(cm.failedCleanupInfos, containerID)
			}
		}
	}
}

// unsafeRetryFailedCleanups retries running clean ups that have failed before.
// It assumes that the manager's lock has already been acquired.
func (cm *containerCleanupManager) unsafeRetryFailedCleanups() []string {
	containerIDsToCleanup := make([]string, len(cm.failedCleanupInfos))

	i := 0
	for containerID := range cm.failedCleanupInfos {
		containerIDsToCleanup[i] = containerID
		i++
	}

	for _, containerID := range containerIDsToCleanup {
		cm.unsafeRetryFailedCleanup(containerID)
	}

	return containerIDsToCleanup
}

// unsafeCleanupStaleContainerCleanupInfos runs the clean up for clean up infos older than staleContainerCleanupInfoAge.
// It assumes that the manager's lock has already been acquired.
func (cm *containerCleanupManager) unsafeCleanupStaleContainerCleanupInfos() []string {
	timestampCutoff := currentNanoTimestampFunc() - int64(staleContainerCleanupInfoAge)

	containerIDsToCleanup := make([]string, 0, cm.pendingCleanupInfos.Len())

	for pair := cm.pendingCleanupInfos.Oldest(); pair != nil; pair = pair.Next() {
		cleanupInfoWithTimestamp := pair.Value.(*containerCleanupInfoWithTimestamp)

		if cleanupInfoWithTimestamp.timestamp > timestampCutoff {
			// this one is not old enough to be cleaned up yet, and all remaining ones are newer than this one, we're done
			break
		}

		containerID := pair.Key.(string)
		containerIDsToCleanup = append(containerIDsToCleanup, containerID)

		klog.Warningf("performing stale clean up for container %q", containerID)
	}

	for _, containerID := range containerIDsToCleanup {
		cm.unsafePerformCleanup(containerID)
	}

	return containerIDsToCleanup
}
