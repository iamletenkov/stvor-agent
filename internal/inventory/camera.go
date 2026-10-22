package inventory

import (
	"regexp"
	"strings"
)

var reVideo = regexp.MustCompile(`^video\d+$`)

// cameras lists V4L2 nodes /dev/video* by /sys/class/video4linux. A UVC
// camera has a second node for metadata (index 1); only the first node of a
// device is a camera row.
func (r *reader) cameras() []Camera {
	var out []Camera
	for _, e := range r.dir("sys/class/video4linux") {
		dev := e.Name()
		if !reVideo.MatchString(dev) {
			continue
		}
		dir := "sys/class/video4linux/" + dev
		if idx := r.line(dir + "/index"); idx != "" && idx != "0" {
			continue
		}
		c := Camera{Device: dev, Name: r.line(dir + "/name")}
		if c.Name == "" {
			c.Name = dev
		}
		c.USBPath = usbPathOf(r.link(dir + "/device"))
		if c.USBPath == "" {
			c.USBPath = usbPathOf(r.link(dir))
		}
		out = append(out, c)
	}
	sortBy(out, func(c Camera) string { return c.Device })
	if len(out) > maxCameras {
		out = out[:maxCameras]
	}
	return out
}

// usbPathOf is the USB device a sysfs path runs through: the last element
// like "1-1.2" in ".../usb1/1-1/1-1.2/1-1.2:1.0/video4linux/video0".
func usbPathOf(target string) string {
	parts := strings.Split(target, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if reUSBDevice.MatchString(parts[i]) {
			return parts[i]
		}
	}
	return ""
}
