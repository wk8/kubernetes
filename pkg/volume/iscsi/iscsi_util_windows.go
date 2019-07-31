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
	"fmt"
	"regexp"
	"strings"

	iscsidsc "github.com/wk8/go-win-iscsidsc"
	"github.com/wk8/go-win-iscsidsc/session"
	"github.com/wk8/go-win-iscsidsc/target"
	"github.com/wk8/go-win-iscsidsc/targetportal"
)

// TODO wkpo un commit propre pour chaque dep, un autre pour le reste??

type ISCSIUtil struct{}

// MakeGlobalPDName returns path of global plugin dir
// TODO wkpo wtf?
func (util *ISCSIUtil) MakeGlobalPDName(iscsi iscsiDisk) string {
	wkLog("MakeGlobalPDName(%v)", iscsi)
	panic(iscsi)
	// TODO wkpo
}

// MakeGlobalVDPDName returns path of global volume device plugin dir
func (util *ISCSIUtil) MakeGlobalVDPDName(iscsi iscsiDisk) string {
	wkLog("MakeGlobalVDPDName(%v)", iscsi)
	panic(iscsi)
	// TODO wkpo
}

// AttachDisk returns devicePath of volume if attach succeeded otherwise returns error
func (util *ISCSIUtil) AttachDisk(b iscsiDiskMounter) (string, error) {
	// TODO wkpo
	wkLog("AttachDisk(%v)", b)
	targetportal.AddIScsiSendTargetPortal()
	session.GetDevicesForIScsiSession()
	target.LoginIscsiTarget()

	// TODO wkpo oldies
	wkLog("AttachDisk(%v)", b)
	return "", iscsidsc.NewWinAPICallError("wkpo", 28)
}

// DetachDisk unmounts and detaches a volume from node
func (util *ISCSIUtil) DetachDisk(c iscsiDiskUnmounter, mntPath string) error {
	// TODO wkpo
	wkLog("DetachDisk(%v, %q)", c, mntPath)
	return iscsidsc.NewWinAPICallError("wkpo", 12)
}

// DetachBlockISCSIDisk removes loopback device for a volume and detaches a volume from node
func (util *ISCSIUtil) DetachBlockISCSIDisk(c iscsiDiskUnmapper, mapPath string) error {
	// TODO wkpo
	wkLog("DetachBlockISCSIDisk(%v, %q)", c, mapPath)
	return iscsidsc.NewWinAPICallError("wkpo", 12)
}

// TODO wkpo what are these??

var ifaceRe = regexp.MustCompile(`.+/iface-([^/]+)/.+`)

func extractDeviceAndPrefix(mntPath string) (string, string, error) {
	ind := strings.LastIndex(mntPath, "/")
	if ind < 0 {
		return "", "", fmt.Errorf("iscsi detach disk: malformatted mnt path: %s", mntPath)
	}
	device := mntPath[(ind + 1):]
	// strip -lun- from mount path
	ind = strings.LastIndex(mntPath, "-lun-")
	if ind < 0 {
		return "", "", fmt.Errorf("iscsi detach disk: malformatted mnt path: %s", mntPath)
	}
	prefix := mntPath[:ind]

	wkLog("extractDeviceAndPrefix(%s) => %q, %q, nil", mntPath, device, prefix)
	return device, prefix, nil
}

func extractIface(mntPath string) (string, bool) {
	reOutput := ifaceRe.FindStringSubmatch(mntPath)
	if reOutput != nil {
		wkLog("extractIface(%s) => %q, true", mntPath, reOutput[1])
		return reOutput[1], true
	}

	wkLog("extractIface(%s) => \"\", false", mntPath)
	return "", false
}

func extractPortalAndIqn(device string) (string, string, error) {
	ind1 := strings.Index(device, "-")
	if ind1 < 0 {
		return "", "", fmt.Errorf("iscsi detach disk: no portal in %s", device)
	}
	portal := device[0:ind1]
	ind2 := strings.Index(device, "iqn.")
	if ind2 < 0 {
		ind2 = strings.Index(device, "eui.")
	}
	if ind2 < 0 {
		return "", "", fmt.Errorf("iscsi detach disk: no iqn in %s", device)
	}
	ind := strings.LastIndex(device, "-lun-")
	iqn := device[ind2:ind]
	wkLog("extractPortalAndIqn(%s) => %q, %q, nil", device, portal, iqn)
	return portal, iqn, nil
}

func wkLogPath() string {
	return "C:/wk.log"
}
