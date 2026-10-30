package ingest

import "strings"

// Channels a visit can come from.
const (
	ChannelDirect   = "direct"
	ChannelSearch   = "search"
	ChannelSocial   = "social"
	ChannelEmail    = "email"
	ChannelPaid     = "paid"
	ChannelReferral = "referral"
)

// searchEngines and socialSites are matched against a referrer's
// hostname, without "www." and "m.", by the label before the TLD: a
// "google" label is Google whatever its country domain.
var (
	searchEngines = []string{"google", "bing", "duckduckgo", "yahoo", "yandex", "baidu", "ecosia", "kagi", "qwant", "startpage", "brave", "naver", "seznam"}
	socialSites   = []string{"facebook", "instagram", "twitter", "x", "t", "linkedin", "lnkd", "reddit", "youtube", "tiktok", "pinterest", "mastodon", "threads", "bsky", "ycombinator", "news.ycombinator", "discord", "telegram", "whatsapp", "snapchat", "vk", "weibo", "quora", "tumblr"}
)

// Channel says where a visit came from: its UTM medium first (what the
// site's own links say), then its referrer's hostname.
func Channel(referrer, utmSource, utmMedium string) string {
	switch m := strings.ToLower(utmMedium); {
	case m == "email" || m == "newsletter" || m == "e-mail":
		return ChannelEmail
	case m == "cpc" || m == "ppc" || m == "paid" || m == "paidsearch" || strings.HasPrefix(m, "paid-") || m == "display" || m == "banner":
		return ChannelPaid
	case m == "social" || m == "social-media" || m == "sm":
		return ChannelSocial
	}
	if referrer == "" {
		if utmSource != "" {
			return ChannelReferral
		}
		return ChannelDirect
	}
	h := strings.TrimPrefix(strings.TrimPrefix(referrer, "www."), "m.")
	if strings.HasPrefix(h, "mail.") || strings.Contains(h, "outlook.") {
		return ChannelEmail
	}
	for _, label := range []string{siteLabel(h), h[:max(0, strings.LastIndexByte(h, '.'))]} {
		for _, s := range searchEngines {
			if label == s {
				return ChannelSearch
			}
		}
		for _, s := range socialSites {
			if label == s {
				return ChannelSocial
			}
		}
	}
	return ChannelReferral
}

// siteLabel is a hostname's label before its public suffix, roughly: the
// second-to-last label, or the third-to-last for two-letter country
// suffixes under a short one (google.co.uk → google).
func siteLabel(h string) string {
	// The last three labels, from the right, without splitting.
	rest, last := cutLast(h)
	rest, second := cutLast(rest)
	_, third := cutLast(rest)
	switch {
	case third != "" && len(last) == 2 && len(second) <= 3:
		return third
	case second != "":
		return second
	}
	return h
}

// cutLast splits a hostname before its last label.
func cutLast(h string) (rest, label string) {
	i := strings.LastIndexByte(h, '.')
	if i < 0 {
		return "", h
	}
	return h[:i], h[i+1:]
}
