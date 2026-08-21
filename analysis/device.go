package analysis

import (
	"regexp"
	"strings"
)

var blockDevicePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^/dev/(sd|hd|vd)[a-z]+[0-9]*$`),
	regexp.MustCompile(`^/dev/xvd[a-z]+[0-9]*$`),
	regexp.MustCompile(`^/dev/nvme[0-9]+n[0-9]+(p[0-9]+)?$`),
	regexp.MustCompile(`^/dev/mmcblk[0-9]+(p[0-9]+)?$`),
	regexp.MustCompile(`^/dev/(loop|nbd|md|rbd|drbd)[0-9]+(p[0-9]+)?$`),
	regexp.MustCompile(`^/dev/dm-[0-9]+$`),
	regexp.MustCompile(`^/dev/(r?disk)[0-9]+(s[0-9]+)?$`),
}

var blockDeviceDirectoryPrefixes = []string{
	"/dev/block/",
	"/dev/disk/",
	"/dev/mapper/",
	"/dev/zvol/",
}

func blockDevicePath(name, directory string, directoryUnknown bool) bool {
	var known bool
	name, known = resolvedPath(directory, name, directoryUnknown)
	if !known {
		return false
	}
	if name == "/dev/root" {
		return true
	}
	if name == "/dev/mapper/control" {
		return false
	}
	for _, prefix := range blockDeviceDirectoryPrefixes {
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
			return true
		}
	}
	for _, pattern := range blockDevicePatterns {
		if pattern.MatchString(name) {
			return true
		}
	}
	return false
}

func hasBlockDevice(arguments []string, directory string, directoryUnknown bool) bool {
	for _, argument := range arguments {
		if blockDevicePath(argument, directory, directoryUnknown) {
			return true
		}
	}
	return false
}
