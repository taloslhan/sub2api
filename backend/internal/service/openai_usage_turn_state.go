package service

import "net/http"

// OpenAIUsageTurnState 是一次真实上游尝试的快照，不用于回放或调度。
// WS 的值属于连接握手；复用连接时不能把它解释为本轮重新签发。
type OpenAIUsageTurnState struct {
	Request          string
	Response         string
	Transport        string
	ConnectionReused *bool
}

func openAIHTTPUsageTurnState(resp *http.Response) *OpenAIUsageTurnState {
	if resp == nil {
		return nil
	}
	state := &OpenAIUsageTurnState{Transport: "http", Response: extractOpenAICodexTurnState(resp.Header)}
	if resp.Request != nil {
		state.Request = extractOpenAICodexTurnState(resp.Request.Header)
	}
	return state
}

// usageTurnState 返回值副本，避免后续轮次改变已进入异步计费队列的快照。
func (l *openAIWSConnLease) usageTurnState() *OpenAIUsageTurnState {
	if l == nil || l.conn == nil {
		return nil
	}
	observed := l.conn.usageStateObserved.Swap(true)
	return openAIWSUsageTurnState(l.conn.handshakeRequestTurnState, l.conn.handshakeHeaders, l.Reused() || observed)
}

// 两种 WS 模式共享握手来源及字节长度口径。
func openAIWSUsageTurnState(request string, response http.Header, reused bool) *OpenAIUsageTurnState {
	return &OpenAIUsageTurnState{
		Request: request, Response: extractOpenAICodexTurnState(response),
		Transport: "ws", ConnectionReused: &reused,
	}
}

func applyUsageTurnState(log *UsageLog, state *OpenAIUsageTurnState) {
	if log == nil || state == nil {
		return
	}
	log.UpstreamRequestTurnState = optionalTrimmedStringPtr(state.Request)
	log.UpstreamResponseTurnState = optionalTrimmedStringPtr(state.Response)
	log.TurnStateTransport = optionalTrimmedStringPtr(state.Transport)
	log.TurnStateConnectionReused = state.ConnectionReused
}
