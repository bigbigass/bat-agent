package main

import "unicode/utf8"

const (
	maxOutputRunes        = 256 * 1024
	outputTruncatedNotice = "[output truncated; showing most recent output]\r\n"
)

func capOutput(value string) string {
	if utf8.RuneCountInString(value) <= maxOutputRunes {
		return value
	}

	noticeRunes := utf8.RuneCountInString(outputTruncatedNotice)
	keepRunes := maxOutputRunes - noticeRunes
	if keepRunes <= 0 {
		return outputTruncatedNotice
	}

	runes := []rune(value)
	return outputTruncatedNotice + string(runes[len(runes)-keepRunes:])
}
