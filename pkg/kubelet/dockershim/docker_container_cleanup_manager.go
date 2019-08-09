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
)

type containerCleanupManager struct {
	cleanupService containerCleanupService
	cleanupInfos   map[string]*containerCleanupInfoWithMetadata
	mutex          *sync.Mutex
}

type containerCleanupInfoWithMetadata struct {
	cleanupInfo *containerCleanupInfo
	// when this cleanup info was added to our state
	timestamp int64
	// how many times we've failed to actually run the cleanup associated
	failureCount int
}

// An interface containing the one of dockerService's methods that we need here;
// allows mocking it in tests.
type containerCleanupService interface {
	performContainerCleanup(containerID string, cleanupInfo *containerCleanupInfo) []error
}

func newDockerContainerCleanupManager(cleanupService containerCleanupService) *containerCleanupManager {
	return &containerCleanupManager{
		cleanupService: cleanupService,
		cleanupInfos:   make(map[string]*containerCleanupInfoWithMetadata),
		mutex:          &sync.Mutex{},
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

		containerIDsToCleanup := cm.unsafeContainerIDsToCleanupOnTick()

		for _, containerID := range containerIDsToCleanup {
			cm.unsafePerformCleanup(containerID)
		}

		if tickChannel != nil {
			tickChannel <- containerIDsToCleanup
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

	if withMetadata, present := cm.cleanupInfos[containerID]; present {
		// shouldn't happen, as we only insert at container creation time
		// but still, let's do the right thing if it does happen, and reset our state
		klog.Errorf("duplicate cleanup info for container ID %q", containerID)

		withMetadata.cleanupInfo = cleanupInfo
		withMetadata.timestamp = currentNanoTimestampFunc()
		withMetadata.failureCount = 0
	} else {
		cm.cleanupInfos[containerID] = &containerCleanupInfoWithMetadata{
			cleanupInfo: cleanupInfo,
			timestamp:   currentNanoTimestampFunc(),
		}
	}
}

// containerIDsToCleanupOnTick returns the IDs of the container IDs to cleanup at every tick,
// i.e. those that already have failed at least once, as well as those that have become stale.
// It assumes that the manager's lock has already been acquired.
func (cm *containerCleanupManager) unsafeContainerIDsToCleanupOnTick() []string {
	timestampCutoff := currentNanoTimestampFunc() - int64(staleContainerCleanupInfoAge)
	containerIDsToCleanup := make([]string, 0, len(cm.cleanupInfos))

	for containerID, withMetadata := range cm.cleanupInfos {
		if withMetadata.failureCount > 0 || withMetadata.timestamp <= timestampCutoff {
			containerIDsToCleanup = append(containerIDsToCleanup, containerID)

			if withMetadata.timestamp <= timestampCutoff {
				klog.Warningf("performing stale clean up for container %q", containerID)
			}
		}
	}

	return containerIDsToCleanup
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
	withMedata, present := cm.cleanupInfos[containerID]
	if !present {
		return
	}

	if errors := cm.cleanupService.performContainerCleanup(containerID, withMedata.cleanupInfo); len(errors) == 0 {
		// the clean up succeeded
		delete(cm.cleanupInfos, containerID)
	} else {
		withMedata.failureCount++

		if withMedata.failureCount >= maxFailures {
			klog.Errorf("unable to clean up container %q, giving up after %v failures", containerID, withMedata.failureCount)
			delete(cm.cleanupInfos, containerID)
		}
	}
}
