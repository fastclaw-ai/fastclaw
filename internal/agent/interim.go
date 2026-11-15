package agent

import (
	"context"
	"strings"
	"unicode"
)

// InterimSender delivers text the model says mid-turn (alongside a tool
// call: "好，我先找 10 个站，再逐个提交", "已提交 3/10") to the person right
// away. IM channels show only the final reply otherwise, so a long task
// looks like silence until it ends. The gateway sets one for IM turns;
// web chat streams events instead and has none.
type InterimSender func(text string)

type interimKey struct{}

// WithInterimSender returns ctx carrying send.
func WithInterimSender(ctx context.Context, send InterimSender) context.Context {
	if send == nil {
		return ctx
	}
	return context.WithValue(ctx, interimKey{}, send)
}

func interimSenderFrom(ctx context.Context) InterimSender {
	send, _ := ctx.Value(interimKey{}).(InterimSender)
	return send
}

// sendInterim delivers text now when the turn has a sender and reports
// whether it did; the caller then leaves text out of the final reply.
func sendInterim(ctx context.Context, text string) bool {
	send := interimSenderFrom(ctx)
	text = strings.TrimSpace(text)
	if send == nil || text == "" {
		return false
	}
	send(text)
	return true
}

// prefersChinese reports whether text is mostly Chinese, for the few
// system-written lines (turn-stop notices, progress pings) that would
// otherwise be English.
func prefersChinese(text string) bool {
	han, latin := 0, 0
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case r < unicode.MaxASCII && unicode.IsLetter(r):
			latin++
		}
	}
	return han > 0 && han*10 >= latin
}

// PrefersChinese is prefersChinese for the gateway's progress pings.
func PrefersChinese(text string) bool { return prefersChinese(text) }
