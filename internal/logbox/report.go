package logbox

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "twocan/internal/proto"
)

// WriteReport writes a downloaded report to dir: the log, decompressed, as
// <code>-<name>.log, and its details as <code>.txt. The report is untrusted:
// the name is reduced to letters, digits, - and _, and decompression stops
// with an error past maxText bytes (keeping what came before).
func WriteReport(dir string, info *pb.ReportInfo, gz io.Reader, maxText int64) (logPath, txtPath string, err error) {
	if !validCode(info.GetCode()) {
		return "", "", fmt.Errorf("report code %q is not valid", info.GetCode())
	}
	logPath = filepath.Join(dir, info.Code+"-"+safeName(info.GetDetails().GetName())+".log")
	txtPath = filepath.Join(dir, info.Code+".txt")

	f, err := os.Create(logPath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	zr, err := gzip.NewReader(gz)
	if errors.Is(err, io.EOF) { // an empty log
		zr = nil
	} else if err != nil {
		return "", "", fmt.Errorf("log is not gzip: %w", err)
	}
	if zr != nil {
		n, err := io.Copy(f, io.LimitReader(zr, maxText+1))
		if err != nil {
			return "", "", err
		}
		if n > maxText {
			if err := f.Truncate(maxText); err != nil {
				return "", "", err
			}
			return logPath, "", fmt.Errorf("log is larger than %d bytes once decompressed; kept the first %d", maxText, maxText)
		}
	}
	if err := os.WriteFile(txtPath, []byte(describe(info)), 0o644); err != nil {
		return "", "", err
	}
	return logPath, txtPath, nil
}

// safeName is name reduced to [A-Za-z0-9_-], at most 32 characters, or
// "device" if nothing is left.
func safeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if b.Len() == 32 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "device"
	}
	return b.String()
}

// describe is a report's details as text, with how far the device's clock
// was from the helper's (to line up logs of several devices).
func describe(info *pb.ReportInfo) string {
	d := info.GetDetails()
	received := time.UnixMilli(info.GetReceivedMs()).UTC()
	var b strings.Builder
	fmt.Fprintf(&b, "code: %s\nreceived: %s\npeer: %s\nname: %s\napp: %s %s\nplatform: %s\n",
		info.GetCode(), received.Format(time.RFC3339), info.GetPeerId(), d.GetName(), d.GetApp(), d.GetVersion(), d.GetPlatform())
	if d.GetDeviceTimeMs() != 0 {
		device := time.UnixMilli(d.GetDeviceTimeMs()).UTC()
		fmt.Fprintf(&b, "device time: %s\nclock difference: %s (helper minus device, including upload time)\n",
			device.Format(time.RFC3339), received.Sub(device).Round(time.Second))
	}
	fmt.Fprintf(&b, "note: %s\n", d.GetNote())
	return b.String()
}
