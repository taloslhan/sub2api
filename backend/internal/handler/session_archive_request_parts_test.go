package handler

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/custom/sessionarchive"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCaptureSessionArchiveRequestPartsDecodesInlineBodiesWithIndependentBudgets(t *testing.T) {
	processed := make(chan sessionarchive.CaptureEvent, 3)
	collector, err := sessionarchive.NewCollector(sessionarchive.CollectorOptions{
		WorkerCount: 1, QueueSize: 4, QueueMaxBytes: 64, PayloadMaxBytes: 3,
		MaxRetries: 0, RetryBackoff: time.Millisecond,
		Processor: sessionarchive.ProcessorFunc(func(_ context.Context, event sessionarchive.CaptureEvent) error {
			processed <- event
			return nil
		}),
	})
	require.NoError(t, err)
	require.NoError(t, collector.Start(context.Background()))

	body := []byte(`{
		"tools":[{"name":"lookup","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"base64","media_type":"text/plain","data":"aGVsbG8="}},
			{"type":"document","source":{"type":"base64","media_type":"text/plain","data":"d29ybGQ="}}
		]}]
	}`)
	captureSessionArchiveRequestParts(collector, sessionarchive.CaptureMeta{Policy: sessionarchive.ResolvedPolicy{
		Enabled: true, CaptureAttachments: true, CaptureTools: true, PayloadMaxBytes: 3,
	}}, service.ContentModerationProtocolAnthropicMessages, body)

	attachments := make([]sessionarchive.CaptureEvent, 0, 2)
	for range 3 {
		select {
		case event := <-processed:
			if event.Meta.Purpose == sessionarchive.PurposeAttachment {
				attachments = append(attachments, event)
			}
		case <-time.After(time.Second):
			t.Fatal("归档请求子项未被处理")
		}
	}
	require.Len(t, attachments, 2)
	for index, event := range attachments {
		require.Equal(t, int64(index+1), event.Meta.SequenceNo)
		require.Equal(t, "text/plain", event.Meta.ContentType)
		require.Equal(t, int64(5), event.Observation.ObservedBytes)
		require.Equal(t, int64(3), event.Observation.StoredBytes, "每个附件应分别获得 3 字节正文预算")
		require.True(t, event.Observation.Truncated)
	}
	require.Equal(t, "hel", string(attachments[0].Observation.StoredPayload))
	require.Equal(t, "wor", string(attachments[1].Observation.StoredPayload))
	require.NoError(t, collector.Shutdown(context.Background()))
}

func TestExtractSessionArchiveRequestPartsUsesProtocolFieldsOnly(t *testing.T) {
	tests := []struct {
		name              string
		protocol          string
		body              string
		attachments       int
		tools             int
		firstInlineType   string
		firstInlineBase64 string
	}{
		{
			name:     "openai chat",
			protocol: service.ContentModerationProtocolOpenAIChat,
			body: `{
				"tools":[{"type":"function","function":{"name":"lookup","parameters":{}}}],
				"messages":[
					{"role":"user","content":[
						{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}},
						{"type":"image_url","image_url":{"url":"https://example.test/image.png"}},
						{"type":"input_image","file_id":"file-1"},
						{"type":"text","url":"https://must-not-be-captured.test","text":"hello"}
					]},
					{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
					{"role":"tool","tool_call_id":"call-1","content":"done"}
				],
				"metadata":{"url":"https://must-not-be-captured.test"}
			}`,
			attachments: 3, tools: 3, firstInlineType: "image/png", firstInlineBase64: "aGVsbG8=",
		},
		{
			name:     "anthropic messages",
			protocol: service.ContentModerationProtocolAnthropicMessages,
			body: `{
				"tools":[{"name":"lookup","input_schema":{"type":"object"}}],
				"messages":[{"role":"user","content":[
					{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"aW1hZ2U="}},
					{"type":"document","source":{"type":"url","url":"https://example.test/doc.pdf"}},
					{"type":"tool_use","id":"toolu-1","name":"lookup","input":{}},
					{"type":"tool_result","tool_use_id":"toolu-1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"cmVzdWx0"}}]}
				]}],
				"unrelated":{"source":{"type":"base64","data":"bm8="}}
			}`,
			attachments: 3, tools: 3, firstInlineType: "image/jpeg", firstInlineBase64: "aW1hZ2U=",
		},
		{
			name:     "openai responses",
			protocol: service.ContentModerationProtocolOpenAIResponses,
			body: `{
				"tools":[{"type":"function","name":"lookup","parameters":{}}],
				"input":[
					{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/webp;base64,d2VicA=="}]},
					{"type":"function_call","call_id":"call-2","name":"lookup","arguments":"{}"},
					{"type":"function_call_output","call_id":"call-2","output":"done"}
				]
			}`,
			attachments: 1, tools: 3, firstInlineType: "image/webp", firstInlineBase64: "d2VicA==",
		},
		{
			name:     "gemini",
			protocol: service.ContentModerationProtocolGemini,
			body: `{
				"tools":[{"functionDeclarations":[{"name":"lookup","parameters":{"type":"object"}}]}],
				"contents":[{"role":"user","parts":[
					{"inlineData":{"mimeType":"audio/wav","data":"d2F2"}},
					{"file_data":{"mime_type":"video/mp4","file_uri":"gs://bucket/video.mp4"}},
					{"functionCall":{"id":"call-3","name":"lookup","args":{}}},
					{"function_response":{"id":"call-3","name":"lookup","response":{"ok":true}}},
					{"text":"url https://must-not-be-captured.test"}
				]}]
			}`,
			attachments: 2, tools: 3, firstInlineType: "audio/wav", firstInlineBase64: "d2F2",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			parts := extractSessionArchiveRequestParts(testCase.protocol, []byte(testCase.body), true, true)
			var attachments, tools []sessionArchiveRequestPart
			for _, part := range parts {
				switch part.purpose {
				case sessionarchive.PurposeAttachment:
					attachments = append(attachments, part)
				case sessionarchive.PurposeTool:
					tools = append(tools, part)
				}
			}
			require.Len(t, attachments, testCase.attachments)
			require.Len(t, tools, testCase.tools)
			require.Equal(t, testCase.firstInlineType, attachments[0].contentType)
			require.Equal(t, testCase.firstInlineBase64, attachments[0].base64Data)
			for _, part := range parts {
				require.NotContains(t, string(part.payload), "must-not-be-captured")
			}
		})
	}
}

func TestExtractSessionArchiveRequestPartsHonorsIndependentPolicies(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"function","function":{"name":"lookup"}}],
		"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/image.png"}}]}]
	}`)

	attachmentOnly := extractSessionArchiveRequestParts(service.ContentModerationProtocolOpenAIChat, body, true, false)
	require.Len(t, attachmentOnly, 1)
	require.Equal(t, sessionarchive.PurposeAttachment, attachmentOnly[0].purpose)
	require.JSONEq(t, `{"type":"image_url","url":"https://example.test/image.png"}`, string(attachmentOnly[0].payload))

	toolOnly := extractSessionArchiveRequestParts(service.ContentModerationProtocolOpenAIChat, body, false, true)
	require.Len(t, toolOnly, 1)
	require.Equal(t, sessionarchive.PurposeTool, toolOnly[0].purpose)

	require.Empty(t, extractSessionArchiveRequestParts("unknown", body, true, true))
}

func TestSplitArchiveDataURIRejectsNonBase64Data(t *testing.T) {
	mediaType, data, ok := splitArchiveDataURI("data:text/plain;base64,aGVsbG8=")
	require.True(t, ok)
	require.Equal(t, "text/plain", mediaType)
	require.Equal(t, "aGVsbG8=", data)

	_, _, ok = splitArchiveDataURI("data:text/plain,hello")
	require.False(t, ok)
}
