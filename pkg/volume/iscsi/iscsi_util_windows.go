// +build windows

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

package iscsi

import (
	iscsidsc "github.com/wk8/go-win-iscsidsc"
)

// TODO wkpo un commit propre pour chaque dep, un autre pour le reste??

type ISCSIUtil struct{}

// MakeGlobalPDName returns path of global plugin dir
func (util *ISCSIUtil) MakeGlobalPDName(iscsi iscsiDisk) string {
	return ""
}

// MakeGlobalVDPDName returns path of global volume device plugin dir
func (util *ISCSIUtil) MakeGlobalVDPDName(iscsi iscsiDisk) string {
	// TODO wkpo
	return ""
}

// AttachDisk returns devicePath of volume if attach succeeded otherwise returns error
func (util *ISCSIUtil) AttachDisk(b iscsiDiskMounter) (string, error) {
	// TODO wkpo
	return "", nil
}

// DetachDisk unmounts and detaches a volume from node
func (util *ISCSIUtil) DetachDisk(c iscsiDiskUnmounter, mntPath string) error {
	// TODO wkpo
	return iscsidsc.NewWinAPICallError("wkpo", 12)
}

// DetachBlockISCSIDisk removes loopback device for a volume and detaches a volume from node
func (util *ISCSIUtil) DetachBlockISCSIDisk(c iscsiDiskUnmapper, mapPath string) error {
	// TODO wkpo
	return nil
}

// TODO wkpo maybe need extractDeviceAndPrefix and extractIface and extractPortalAndIqn?
