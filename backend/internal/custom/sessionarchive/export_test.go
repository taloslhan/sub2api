package sessionarchive

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeExportContent struct {
	record ContentRecord
	body   []byte
	err    error
}

func TestSFTPreflightCountsRequestsAndRejectsIncompleteSamples(t *testing.T) {
	available := func(body string) fakeExportContent {
		return fakeExportContent{record: ContentRecord{Ref: BlobRef{Available: true}}, body: []byte(body)}
	}
	contents := map[int64]map[string]fakeExportContent{
		1: {"request": available(`{"messages":[{"role":"user","content":"hello"}]}`), "response": available(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`)},
		2: {"request": {record: ContentRecord{Ref: BlobRef{Available: false}}}, "response": available(`{"ok":true}`)},
		3: {"request": available(`{"ok":true}`), "response": {record: ContentRecord{Ref: BlobRef{Available: true, Truncated: true}}, body: []byte(`{"partial":`)}},
		4: {"request": available(`null`), "response": available(`{"ok":true}`)},
		5: {"request": available(`{"ok":true}`), "response": available(`not-json`)},
		6: {"request": {err: errors.New("object storage unavailable")}, "response": available(`{"ok":true}`)},
		7: {"request": {err: errMultipleContentParts}, "response": available(`{"ok":true}`)},
	}
	reader := func(_ context.Context, requestID int64, kind string) (ContentRecord, []byte, error) {
		content := contents[requestID][kind]
		return content.record, content.body, content.err
	}
	requests := []Request{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}, {ID: 5}, {ID: 6}, {ID: 7}}
	result := exportPreflightResult{MatchedSessions: 2, SkippedReasons: make(map[string]int)}

	tallySFTRequests(context.Background(), requests, reader, &result)

	require.Equal(t, 2, result.MatchedSessions)
	require.Equal(t, 1, result.EligibleSamples)
	require.Equal(t, 6, result.SkippedSamples)
	require.Equal(t, map[string]int{
		"request_unavailable":       1,
		"response_truncated":        1,
		"request_invalid_json":      1,
		"response_invalid_json":     1,
		"request_read_failed":       1,
		"request_unsupported_parts": 1,
	}, result.SkippedReasons)
	require.Equal(t, len(requests), result.EligibleSamples+result.SkippedSamples)
}

func TestReadSFTSampleReturnsNormalizedMessagesOnlyWhenBothBlobsAreUsable(t *testing.T) {
	reader := func(_ context.Context, _ int64, kind string) (ContentRecord, []byte, error) {
		body := []byte(`{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`)
		if kind == "response" {
			body = []byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
		}
		return ContentRecord{Ref: BlobRef{Available: true}}, body, nil
	}

	sample, reason := readSFTSample(context.Background(), 42, reader)

	require.Empty(t, reason)
	require.Len(t, sample.Messages, 2)
	require.Len(t, sample.Tools, 1)
	encoded, err := json.Marshal(sample)
	require.NoError(t, err)
	require.JSONEq(t, `{"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`, string(encoded))
}

func TestNormalizeSFTSampleRejectsArbitraryJSONAndSupportsKnownProtocols(t *testing.T) {
	_, reason := normalizeSFTSample([]byte(`{"ok":true}`), []byte(`{"ok":true}`))
	require.Equal(t, "request_unsupported_schema", reason)

	tests := []struct {
		name     string
		request  string
		response string
	}{
		{
			name:     "responses",
			request:  `{"input":"hello"}`,
			response: `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`,
		},
		{
			name:     "gemini",
			request:  `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			response: `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}]}`,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			sample, reason := normalizeSFTSample([]byte(testCase.request), []byte(testCase.response))
			require.Empty(t, reason)
			require.Len(t, sample.Messages, 2)
		})
	}
}

func TestNormalizeSFTSamplePreservesLegitimateRepeatedMessages(t *testing.T) {
	sample, reason := normalizeSFTSample(
		[]byte(`{"messages":[{"role":"user","content":"again"},{"role":"assistant","content":"again"},{"role":"user","content":"again"}]}`),
		[]byte(`{"choices":[{"message":{"role":"assistant","content":"again"}}]}`),
	)

	require.Empty(t, reason)
	require.Len(t, sample.Messages, 4)
}

func TestNormalizeSFTSampleCanonicalizesAnthropicToolsAndToolMessages(t *testing.T) {
	sample, reason := normalizeSFTSample(
		[]byte(`{
			"system":"follow policy",
			"tools":[{"name":"lookup","description":"Lookup data","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}],
			"messages":[
				{"role":"user","content":"hello"},
				{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"toolu-1","name":"lookup","input":{"q":"x"}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu-1","content":{"ok":true}}]}
			]
		}`),
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"done"}]}`),
	)

	require.Empty(t, reason)
	encoded, err := json.Marshal(sample)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"messages":[
			{"role":"system","content":"follow policy"},
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"checking","tool_calls":[{"id":"toolu-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},
			{"role":"tool","tool_call_id":"toolu-1","content":"{\"ok\":true}"},
			{"role":"assistant","content":"done"}
		],
		"tools":[{"type":"function","function":{"name":"lookup","description":"Lookup data","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}]
	}`, string(encoded))
}

func TestNormalizeSFTSampleCanonicalizesResponsesAndDeduplicatesOnlyToolIDs(t *testing.T) {
	sample, reason := normalizeSFTSample(
		[]byte(`{
			"tools":[{"type":"function","name":"lookup","description":"Lookup data","parameters":{"type":"object"}}],
			"input":[
				{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]},
				{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"x\"}"},
				{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"x\"}"},
				{"type":"function_call_output","call_id":"call-1","output":{"ok":true}},
				{"type":"function_call_output","call_id":"call-1","output":{"ok":true}}
			]
		}`),
		[]byte(`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`),
	)

	require.Empty(t, reason)
	require.Len(t, sample.Messages, 5, "两条合法重复 user 消息必须保留，重复 tool call/result ID 各只保留一次")
	require.Equal(t, sample.Messages[0], sample.Messages[1])
	require.Len(t, sample.Tools, 1)
	encoded, err := json.Marshal(sample.Messages[2:4])
	require.NoError(t, err)
	require.JSONEq(t, `[
		{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},
		{"role":"tool","tool_call_id":"call-1","content":"{\"ok\":true}"}
	]`, string(encoded))
}

func TestNormalizeSFTSampleCanonicalizesGeminiAndPairsMissingIDsByName(t *testing.T) {
	sample, reason := normalizeSFTSample(
		[]byte(`{
			"systemInstruction":{"parts":[{"text":"follow policy"}]},
			"tools":[{"functionDeclarations":[{"name":"lookup","description":"Lookup data","parameters":{"type":"object"}}]}],
			"contents":[
				{"role":"user","parts":[{"text":"hello"}]},
				{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]},
				{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"ok":true}}}]}
			]
		}`),
		[]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]}}]}`),
	)

	require.Empty(t, reason)
	encoded, err := json.Marshal(sample)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"messages":[
			{"role":"system","content":"follow policy"},
			{"role":"user","content":"hello"},
			{"role":"assistant","tool_calls":[{"id":"gemini_call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},
			{"role":"tool","tool_call_id":"gemini_call_1","content":"{\"ok\":true}"},
			{"role":"assistant","content":"done"}
		],
		"tools":[{"type":"function","function":{"name":"lookup","description":"Lookup data","parameters":{"type":"object"}}}]
	}`, string(encoded))
}
