package ingest

import "strings"

// Agent is what a user agent string tells about a visitor's browser.
type Agent struct {
	Browser, BrowserVersion string
	OS, OSVersion           string
	// Device is desktop, mobile or tablet.
	Device string
	// Bot is set for crawlers, monitors, previewers and HTTP libraries:
	// their requests aren't visits.
	Bot bool
}

// botMarks are lower-case fragments only automated clients send.
var botMarks = []string{
	"bot", "crawl", "spider", "slurp", "headless", "lighthouse", "pingdom", "monitor",
	"preview", "facebookexternalhit", "curl/", "wget/", "python-", "go-http-client",
	"httpclient", "java/", "okhttp", "axios/", "node-fetch", "phantomjs", "scrapy",
}

// browsers are checked in order: Chromium-based browsers also say Chrome
// and Safari, so they come before them.
var browsers = []struct{ mark, name string }{
	{"Edg/", "Edge"}, {"EdgA/", "Edge"}, {"EdgiOS/", "Edge"}, {"OPR/", "Opera"},
	{"SamsungBrowser/", "Samsung Internet"}, {"YaBrowser/", "Yandex"}, {"Vivaldi/", "Vivaldi"},
	{"Firefox/", "Firefox"}, {"FxiOS/", "Firefox"}, {"CriOS/", "Chrome"}, {"Chrome/", "Chrome"},
	{"Version/", "Safari"},
}

// ParseAgent reads a user agent string. It knows the common browsers and
// systems and calls the rest "Other"; it allocates nothing but its
// result's strings, which point into ua.
func ParseAgent(ua string) Agent {
	if ua == "" {
		return Agent{Bot: true}
	}
	lower := strings.ToLower(ua)
	for _, m := range botMarks {
		if strings.Contains(lower, m) {
			return Agent{Bot: true}
		}
	}
	var a Agent
	a.Browser = "Other"
	for _, b := range browsers {
		if i := strings.Index(ua, b.mark); i >= 0 {
			a.Browser, a.BrowserVersion = b.name, major(ua[i+len(b.mark):])
			break
		}
	}
	switch {
	case strings.Contains(ua, "iPad"):
		a.OS, a.OSVersion, a.Device = "iOS", osVersion(ua, "OS "), "tablet"
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPod"):
		a.OS, a.OSVersion, a.Device = "iOS", osVersion(ua, "OS "), "mobile"
	case strings.Contains(ua, "Android"):
		a.OS, a.OSVersion, a.Device = "Android", osVersion(ua, "Android "), "mobile"
		if !strings.Contains(ua, "Mobile") {
			a.Device = "tablet"
		}
	case strings.Contains(ua, "Windows"):
		a.OS, a.Device = "Windows", "desktop"
	case strings.Contains(ua, "Mac OS X") || strings.Contains(ua, "Macintosh"):
		a.OS, a.OSVersion, a.Device = "macOS", osVersion(ua, "Mac OS X "), "desktop"
	case strings.Contains(ua, "CrOS"):
		a.OS, a.Device = "ChromeOS", "desktop"
	case strings.Contains(ua, "Linux"):
		a.OS, a.Device = "Linux", "desktop"
	default:
		a.OS, a.Device = "Other", "desktop"
	}
	return a
}

// major is the leading number of a version.
func major(s string) string {
	i := 0
	for i < len(s) && i < 6 && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// osVersion is the major version after mark (iOS writes 17_4, macOS 10_15).
func osVersion(ua, mark string) string {
	i := strings.Index(ua, mark)
	if i < 0 {
		return ""
	}
	return major(ua[i+len(mark):])
}
