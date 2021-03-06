package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	//"github.com/docker-slim/docker-slim/internal/app/sensor/monitors/fanotify"
	"github.com/docker-slim/docker-slim/pkg/ipc/command"
	"github.com/docker-slim/docker-slim/pkg/report"

	log "github.com/Sirupsen/logrus"
)

func processReports(mountPoint string,
	fanReport *report.FanMonitorReport,
	ptReport *report.PtMonitorReport,
	peReport *report.PeMonitorReport,
	cmd *command.StartMonitor) {

	fileCount := 0
	for _, processFileMap := range fanReport.ProcessFiles {
		fileCount += len(processFileMap)
	}
	fileList := make([]string, 0, fileCount)
	for _, processFileMap := range fanReport.ProcessFiles {
		for fpath := range processFileMap {
			fileList = append(fileList, fpath)
		}
	}

	log.Debugf("processReports(): len(fanReport.ProcessFiles)=%v / fileCount=%v", len(fanReport.ProcessFiles), fileCount)

	allFilesMap := findSymlinks(fileList, mountPoint)
	saveResults(fanReport, allFilesMap, ptReport, peReport, cmd)
}

func getProcessChildren(pid int, targetPidList map[int]bool, processChildrenMap map[int][]int) {
	if children, ok := processChildrenMap[pid]; ok {
		for _, cpid := range children {
			targetPidList[cpid] = true
			getProcessChildren(cpid, targetPidList, processChildrenMap)
		}
	}
}

/* use - TBD
func findTargetAppProcesses(rootPidList []int, processChildrenMap map[int][]int) map[int]bool {
	var targetPidList map[int]bool

	for _, pid := range rootPidList {
		targetPidList[pid] = true
		getProcessChildren(pid, targetPidList, processChildrenMap)
	}

	return targetPidList
}
*/

/* use - TBD
func filterFileEvents(fileEvents map[fanotify.Event]bool, targetPidList map[int]bool) []string {
	var files []string
	for evt := range fileEvents {
		if _, ok := targetPidList[int(evt.Pid)]; ok {
			files = append(files, evt.File)
		}
	}

	return files
}
*/

///////////////////////////////////////////////////////////////////////////////////////

func findSymlinks(files []string, mp string) map[string]*report.ArtifactProps {
	log.Debugf("findSymlinks(%v,%v)", len(files), mp)

	result := make(map[string]*report.ArtifactProps, 0)

	//getting the root device is a leftover from the legacy code (not really necessary anymore)
	devID, err := getFileDevice(mp)
	if err != nil {
		log.Debugf("findSymlinks - no device ID (%v)", err)
		return result
	}

	log.Debugf("findSymlinks - deviceId=%v", devID)

	inodes, devices := filesToInodesNative(files)
	log.Debugf("findSymlinks - len(inodes)=%v len(devices)=%v", len(inodes), len(devices))

	inodeToFiles := make(map[uint64][]string)

	//native filepath.Walk is a bit slow (compared to the "find" command)
	//but it's fast enough for now
	filepath.Walk(mp, func(fullName string, fileInfo os.FileInfo, err error) error {
		if strings.HasPrefix(fullName, "/proc/") {
			log.Debugf("findSymlinks: skipping /proc file system objects...")
			return filepath.SkipDir
		}

		if strings.HasPrefix(fullName, "/sys/") {
			log.Debugf("findSymlinks: skipping /sys file system objects...")
			return filepath.SkipDir
		}

		if strings.HasPrefix(fullName, "/dev/") {
			log.Debugf("findSymlinks: skipping /dev file system objects...")
			return filepath.SkipDir
		}

		if err != nil {
			log.Debugf("findSymlinks: error accessing %q: %v\n", fullName, err)
			//just ignore the error and keep going
			return nil
		}

		if fileInfo.Sys() == nil {
			log.Debugf("findSymlinks: fileInfo.Sys() is nil (ignoring)")
			return nil
		}

		sysStatInfo, ok := fileInfo.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("findSymlinks - could not convert fileInfo to Stat_t for %s", fullName)
		}

		if _, ok := devices[uint64(sysStatInfo.Dev)]; !ok {
			log.Debugf("findSymlinks: ignoring %v (by device id - %v)", fullName, sysStatInfo.Dev)
			//NOTE:
			//don't return filepath.SkipDir for everything
			//because we might still need other files in the dir
			//return filepath.SkipDir
			//example: "/etc/hostname" Docker mounts from another device
			//NOTE:
			//can move the checks for /dev, /sys and /proc here too
			return nil
		}

		if fileInfo.Mode()&os.ModeSymlink != 0 {
			if info, err := getFileSysStats(fullName); err == nil {

				if _, ok := inodes[info.Ino]; ok {
					//not using the inode for the link (using the target inode instead)
					inodeToFiles[info.Ino] = append(inodeToFiles[info.Ino], fullName)
				} else {
					//log.Debugf("findSymlinks - don't care about this symlink (%s)",fullName)
				}

			} else {
				log.Infof("findSymlinks - could not get target stats info for %v", fullName)
			}

		} else {
			if _, ok := inodes[sysStatInfo.Ino]; ok {
				inodeToFiles[sysStatInfo.Ino] = append(inodeToFiles[sysStatInfo.Ino], fullName)
			} else {
				//log.Debugf("findSymlinks - don't care about this file (%s)",fullName)
			}
		}

		return nil
	})

	log.Debugf("findSymlinks - len(inodeToFiles)=%v", len(inodeToFiles))

	for inodeID := range inodes {
		v := inodeToFiles[inodeID]
		for _, f := range v {
			//result[f] = inodeID
			result[f] = nil
		}
	}

	return result
}

func filesToInodesNative(files []string) (map[uint64]struct{}, map[uint64]struct{}) {
	inodes := map[uint64]struct{}{}
	devices := map[uint64]struct{}{}

	for _, fullName := range files {
		info, err := getFileSysStats(fullName)
		if err != nil {
			log.Debugf("filesToInodesNative - could not get inode for %s", fullName)
			continue
		}

		inodes[info.Ino] = struct{}{}
		devices[uint64(info.Dev)] = struct{}{}
	}

	return inodes, devices
}

func getFileSysStats(fullName string) (*syscall.Stat_t, error) {
	statInfo, err := os.Stat(fullName)
	if err != nil {
		return nil, err
	}

	sysStatInfo, ok := statInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("failed to get system stat info for %s", fullName)
	}

	return sysStatInfo, nil
}

func getFileDevice(fullName string) (uint64, error) {
	info, err := getFileSysStats(fullName)
	if err != nil {
		return 0, err
	}

	return uint64(info.Dev), nil
}

/* use - TBD
func getFileInode(fullName string) (uint64, error) {
	info, err := getFileSysStats(fullName)
	if err != nil {
		return 0, err
	}

	log.Debugf("getFileInode(%s) => %v", fullName, info)

	return info.Ino, nil
}
*/
