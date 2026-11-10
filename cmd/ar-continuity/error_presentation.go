package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

var channelEndpointName = regexp.MustCompile(`(?i)(?:https?://)?(?:[a-z0-9-]+\.)*agentrouter\.org(?::[0-9]+)?`)
var channelProviderName = regexp.MustCompile(`(?i)agentrouter`)
var channelAbbreviation = regexp.MustCompile(`\bAR\b`)

// Format errors only. Successful output and request payloads are not scanned.
func channelErrorMessage(message string) string {
	var result strings.Builder
	start := 0
	// Trace identifiers are opaque; retain them exactly for diagnostics.
	for _, match := range pricingTraceID.FindAllStringIndex(message, -1) {
		result.WriteString(channelErrorText(message[start:match[0]]))
		result.WriteString(message[match[0]:match[1]])
		start = match[1]
	}
	result.WriteString(channelErrorText(message[start:]))
	return result.String()
}

func channelErrorText(message string) string {
	message = channelEndpointName.ReplaceAllString(message, "渠道接口")
	message = channelProviderName.ReplaceAllString(message, "渠道")
	return channelAbbreviation.ReplaceAllString(message, "渠道")
}

func publicRPCError(e *rpcError) rpcError {
	result := *e
	if len(result.Code) >= 3 && strings.EqualFold(result.Code[:3], "ar_") {
		result.Code = "channel_" + result.Code[3:]
	}
	result.Message = channelErrorMessage(result.Message)
	return result
}

func (e *rpcError) MarshalJSON() ([]byte, error) {
	type wireError rpcError
	return json.Marshal(wireError(publicRPCError(e)))
}
