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
	"github.com/pkg/errors"
	"regexp"
	"strconv"
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
	if err := logIntoPortals(b); err != nil {
		return "", err
	}

	sessionID, err := createOrFindSession(b)
	if err != nil {
		return "", err
	}

	device, err := findDevice(b, sessionID)
	if err != nil {
		return "", nil
	}

	if err = createVolumeIfNecessary(b, device); err != nil {
		// TODO wkpo return from createVolumeIfNecessary directement?
		return "", err
	}
}

// TODO wkpo move to EOF
// TODO wkpo name?
// TODO wkpo comment?
func createVolumeIfNecessary(b iscsiDiskMounter, device *iscsidsc.Device) error {

}

// logIntoPortals logs into all of the requested iSCSI portals.
func logIntoPortals(b iscsiDiskMounter) error {
	discoveryLoginOptions := &iscsidsc.LoginOptions{}
	if b.chapDiscovery {
		if err := addChapLoginOptions(b, discoveryLoginOptions, "discovery.sendtargets"); err != nil {
			return err
		}
	}

	// TODO wkpo c bon ca? multipath?
	for _, portalName := range b.Portals {
		portal := &iscsidsc.Portal{
			SymbolicName: portalName,
			Address:      portalName,
			// TODO wkpo port?
		}

		// TODO wkpo security flags?
		if err := targetportal.AddIScsiSendTargetPortal(nil, nil, discoveryLoginOptions, nil, portal); err != nil {
			return errors.Wrapf(err, "Unable to log into iSCSI portal %q", portalName)
		}
	}

	return nil
}

// TODO wkpo multipath?
// createOrFindSession logs into the requested target and returns the ID of the newly created session;
// or else if a session to that target already exists, retrieves its ID and returns it.
func createOrFindSession(b iscsiDiskMounter) (*iscsidsc.SessionID, error) {
	sessionLoginOptions := &iscsidsc.LoginOptions{}
	if b.chapSession {
		if err := addChapLoginOptions(b, sessionLoginOptions, "node.session"); err != nil {
			return nil, err
		}
	}
	sessionID, _, err := target.LoginIscsiTarget(b.Iqn, false, nil, nil, nil,
		nil, sessionLoginOptions, nil, false)
	if err != nil {
		// TODO wkpo comment with relevant link...?
		// TODO wkpo constant?
		// TODO wkpo separate function?
		if winAPIErr, ok := err.(*iscsidsc.WinAPICallError); ok && winAPIErr.HexCode() == "0xEFFF003F" {
			// we're already logged into the target, let's find the existing session
			sessions, err := session.GetIScsiSessionList()
			if err != nil {
				return nil, errors.Wrap(err, "Unable to get the list of existing iSCSI sessions")
			}

			for _, s := range sessions {
				if s.TargetName == b.Iqn {
					sessionID = &s.SessionID
					break
				}
			}

			if sessionID == nil {
				// we didn't find an existing session for that target
				return nil, fmt.Errorf("Unable to find existing iSCSI session for target %q", b.Iqn)
			}
		} else {
			return nil, errors.Wrapf(err, "Unable to log into iSCSI target %q", b.Iqn)
		}
	}

	return sessionID, nil
}

func addChapLoginOptions(b iscsiDiskMounter, loginOptions *iscsidsc.LoginOptions, secretPrefix string) error {
	authType := iscsidsc.NoAuthAuthType
	var (
		username *string
		password *string
	)
	if b.chapDiscovery {
		authType = iscsidsc.CHAPAuthType
		if u := b.secret[secretPrefix+".auth.username"]; len(u) > 0 {
			username = &u
		}
		if p := b.secret[secretPrefix+".auth.password"]; len(p) > 0 {
			password = &p
		}

		if len(b.secret[secretPrefix+".auth.username_in"]) > 0 ||
			len(b.secret[secretPrefix+".auth.password_in"]) > 0 {
			return fmt.Errorf("Mutual CHAP is not supported on Windows")
		}
	}

	loginOptions.AuthType = &authType
	loginOptions.Username = username
	loginOptions.Password = password

	return nil
}

// findDevice looks for the requested LUN amongst the ones proposed by the given iSCSI session.
func findDevice(b iscsiDiskMounter, sessionID *iscsidsc.SessionID) (*iscsidsc.Device, error) {
	devices, err := session.GetDevicesForIScsiSession(*sessionID)
	if err != nil {
		return nil, errors.Wrapf(err, "Unable to get the list of devices for iSCI session %v on target %q",
			*sessionID, b.Iqn)
	}

	// and look for the LUN we want
	var device *iscsidsc.Device
	lun, err := parseLun(b.Lun)
	if err != nil {
		return nil, err
	}
	for _, d := range devices {
		if d.ScsiAddress.Lun == lun {
			device = &d
			break
		}
	}

	if device == nil {
		luns := make([]uint8, len(devices))
		for i, d := range devices {
			luns[i] = d.ScsiAddress.Lun
		}
		err = fmt.Errorf("Unable to find LUN %v on iSCSI target %q, found LUNS: %v",
			lun, b.Iqn, luns)
	}
	return device, err
}

// TODO wkpo unit test with negative value?
func parseLun(lunStr string) (uint8, error) {
	lunInt64, err := strconv.ParseInt(lunStr, 10, 8)
	if err != nil {
		return 0, errors.Wrapf(err, "LUN is not an int8: %q", lunStr)
	}
	return uint8(lunInt64), nil
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
