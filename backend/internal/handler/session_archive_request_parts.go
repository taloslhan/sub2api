package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/custom/sessionarchive"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/tidwall/gjson"
)

type sessionArchiveRequestPart struct {
	kind        sessionarchive.EventKind
	purpose     sessionarchive.BlobPurpose
	contentType string
	payload     []byte
	base64Data  string
}

type sessionArchiveRequestPartCapture interface {
	TryCaptureBytes(sessionarchive.CaptureMeta, []byte) sessionarchive.CaptureResult
	NewSink(sessionarchive.CaptureMeta) *sessionarchive.CaptureSink
}

// captureSessionArchiveRequestParts 只识别四类公开协议的明确附件与工具字段。
// 外链只保存引用，内联 base64 通过独立 sink 解码，因此每一项都有独立正文预算。
func captureSessionArchiveRequestParts(archive sessionArchiveRequestPartCapture, base sessionarchive.CaptureMeta, protocol string, body []byte) {
	if archive == nil || (!base.Policy.CaptureAttachments && !base.Policy.CaptureTools) {
		return
	}
	parts := extractSessionArchiveRequestParts(protocol, body, base.Policy.CaptureAttachments, base.Policy.CaptureTools)
	attachmentSequence, toolSequence := int64(0), int64(0)
	for _, part := range parts {
		sequence := &attachmentSequence
		if part.purpose == sessionarchive.PurposeTool {
			sequence = &toolSequence
		}
		*sequence = *sequence + 1
		meta := withArchiveKind(base, part.kind, part.purpose, *sequence)
		meta.ContentType = part.contentType
		meta.Direction = "client_to_gateway"
		if part.base64Data == "" {
			archive.TryCaptureBytes(meta, part.payload)
			continue
		}
		sink := archive.NewSink(meta)
		if sink == nil {
			continue
		}
		decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(part.base64Data))
		buffer := make([]byte, 32*1024)
		decodeErr := error(nil)
		for {
			read, err := decoder.Read(buffer)
			if read > 0 {
				_, _ = sink.Append(buffer[:read])
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					decodeErr = err
				}
				break
			}
		}
		if decodeErr != nil {
			sink.Abort()
			continue
		}
		sink.Finish()
	}
}

func extractSessionArchiveRequestParts(protocol string, body []byte, captureAttachments, captureTools bool) []sessionArchiveRequestPart {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return nil
	}
	parts := make([]sessionArchiveRequestPart, 0, 8)
	addTool := func(value gjson.Result) {
		if captureTools && value.IsObject() && json.Valid([]byte(value.Raw)) {
			parts = append(parts, sessionArchiveRequestPart{kind: sessionarchive.EventTool, purpose: sessionarchive.PurposeTool, contentType: "application/json", payload: []byte(value.Raw)})
		}
	}
	addReference := func(referenceType, key, value string) {
		if !captureAttachments || strings.TrimSpace(value) == "" {
			return
		}
		payload, err := json.Marshal(map[string]string{"type": referenceType, key: value})
		if err == nil {
			parts = append(parts, sessionArchiveRequestPart{kind: sessionarchive.EventAttachment, purpose: sessionarchive.PurposeAttachment, contentType: "application/json", payload: payload})
		}
	}
	addInline := func(contentType, data string) {
		if !captureAttachments || strings.TrimSpace(data) == "" {
			return
		}
		parts = append(parts, sessionArchiveRequestPart{kind: sessionarchive.EventAttachment, purpose: sessionarchive.PurposeAttachment, contentType: firstNonEmptyString(strings.TrimSpace(contentType), "application/octet-stream"), base64Data: data})
	}
	handleURL := func(referenceType, value string) {
		if mediaType, data, ok := splitArchiveDataURI(value); ok {
			addInline(mediaType, data)
			return
		}
		addReference(referenceType, "url", value)
	}

	handleOpenAIContent := func(content gjson.Result) {
		if !content.IsArray() {
			return
		}
		content.ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "image_url":
				imageURL := item.Get("image_url")
				if imageURL.IsObject() {
					handleURL("image_url", imageURL.Get("url").String())
				} else {
					handleURL("image_url", imageURL.String())
				}
			case "input_image":
				if fileID := item.Get("file_id").String(); fileID != "" {
					addReference("file_id", "file_id", fileID)
				} else {
					handleURL("input_image", firstNonEmptyString(item.Get("image_url").String(), item.Get("url").String()))
				}
			case "input_file":
				if fileID := item.Get("file_id").String(); fileID != "" {
					addReference("file_id", "file_id", fileID)
				} else if fileURL := firstNonEmptyString(item.Get("file_url").String(), item.Get("url").String()); fileURL != "" {
					handleURL("input_file", fileURL)
				} else if fileData := item.Get("file_data").String(); fileData != "" {
					if mediaType, data, ok := splitArchiveDataURI(fileData); ok {
						addInline(mediaType, data)
					}
				}
			}
			return true
		})
	}

	switch protocol {
	case service.ContentModerationProtocolOpenAIChat:
		root.Get("tools").ForEach(func(_, tool gjson.Result) bool { addTool(tool); return true })
		root.Get("messages").ForEach(func(_, message gjson.Result) bool {
			handleOpenAIContent(message.Get("content"))
			message.Get("tool_calls").ForEach(func(_, call gjson.Result) bool { addTool(call); return true })
			if call := message.Get("function_call"); call.Exists() {
				addTool(call)
			}
			if message.Get("role").String() == "tool" && message.Get("tool_call_id").String() != "" {
				addTool(message)
			}
			return true
		})

	case service.ContentModerationProtocolAnthropicMessages:
		root.Get("tools").ForEach(func(_, tool gjson.Result) bool { addTool(tool); return true })
		root.Get("messages").ForEach(func(_, message gjson.Result) bool {
			message.Get("content").ForEach(func(_, block gjson.Result) bool {
				switch block.Get("type").String() {
				case "image", "document":
					source := block.Get("source")
					switch source.Get("type").String() {
					case "base64":
						addInline(source.Get("media_type").String(), source.Get("data").String())
					case "url":
						handleURL(block.Get("type").String(), source.Get("url").String())
					}
				case "tool_use", "tool_result":
					addTool(block)
					if block.Get("type").String() == "tool_result" {
						block.Get("content").ForEach(func(_, resultBlock gjson.Result) bool {
							if resultBlock.Get("type").String() == "image" || resultBlock.Get("type").String() == "document" {
								source := resultBlock.Get("source")
								if source.Get("type").String() == "base64" {
									addInline(source.Get("media_type").String(), source.Get("data").String())
								} else if source.Get("type").String() == "url" {
									handleURL(resultBlock.Get("type").String(), source.Get("url").String())
								}
							}
							return true
						})
					}
				}
				return true
			})
			return true
		})

	case service.ContentModerationProtocolOpenAIResponses:
		root.Get("tools").ForEach(func(_, tool gjson.Result) bool { addTool(tool); return true })
		root.Get("input").ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "message":
				handleOpenAIContent(item.Get("content"))
			case "function_call", "function_call_output":
				addTool(item)
			}
			return true
		})

	case service.ContentModerationProtocolGemini:
		root.Get("tools").ForEach(func(_, tool gjson.Result) bool {
			declarations := tool.Get("functionDeclarations")
			if !declarations.Exists() {
				declarations = tool.Get("function_declarations")
			}
			declarations.ForEach(func(_, declaration gjson.Result) bool { addTool(declaration); return true })
			return true
		})
		handleGeminiParts := func(content gjson.Result) {
			content.Get("parts").ForEach(func(_, part gjson.Result) bool {
				inline := part.Get("inlineData")
				if !inline.Exists() {
					inline = part.Get("inline_data")
				}
				if inline.Exists() {
					addInline(firstNonEmptyString(inline.Get("mimeType").String(), inline.Get("mime_type").String()), inline.Get("data").String())
				}
				file := part.Get("fileData")
				if !file.Exists() {
					file = part.Get("file_data")
				}
				if file.Exists() {
					addReference("file_uri", "uri", firstNonEmptyString(file.Get("fileUri").String(), file.Get("file_uri").String()))
				}
				for _, key := range []string{"functionCall", "function_call", "functionResponse", "function_response"} {
					if value := part.Get(key); value.Exists() {
						addTool(value)
						break
					}
				}
				return true
			})
		}
		root.Get("contents").ForEach(func(_, content gjson.Result) bool { handleGeminiParts(content); return true })
		for _, key := range []string{"systemInstruction", "system_instruction"} {
			if value := root.Get(key); value.Exists() {
				handleGeminiParts(value)
			}
		}
	}
	return parts
}

func splitArchiveDataURI(value string) (string, string, bool) {
	if !strings.HasPrefix(value, "data:") {
		return "", "", false
	}
	header, data, found := strings.Cut(value[len("data:"):], ",")
	if !found {
		return "", "", false
	}
	segments := strings.Split(header, ";")
	if len(segments) < 2 || !strings.EqualFold(segments[len(segments)-1], "base64") {
		return "", "", false
	}
	mediaType := strings.TrimSpace(segments[0])
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return mediaType, data, true
}
