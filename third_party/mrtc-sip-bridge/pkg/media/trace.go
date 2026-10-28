package media

import (
	"time"
)

// traceInterval is one log line per second of media time (50 ticks of 20 ms).
const traceInterval = 50

// traceTick emits one per-second DEBUG line with the media counters and playout buffer depths.
// It exists to locate growing media buffers in live calls: compare the sip_rx / rtc_written deltas
// between consecutive lines (both should advance by ~50 per second), watch the jitter depths (frames
// x 20 ms) and write_us (a WriteOpus call that starts taking longer means the RTC send path is
// backing up). No media content is ever recorded.
func (b *bridge) traceTick(now time.Time) {
	n := b.traceN.Add(1)
	if n%traceInterval != 0 {
		return
	}
	sipLost, sipLate := b.jSIP.stats()
	rtcLost, rtcLate := b.jRTC.stats()
	ev := b.log.Debug().
		Uint64("trace_n", n).
		Uint64("sip_rx", b.cnt.sipRx.Load()).
		Uint64("sip_tx", b.cnt.sipTx.Load()).
		Uint64("rtc_rx", b.cnt.rtcRx.Load()).
		Uint64("rtc_written", b.writtenRTC.Load()).
		Uint64("concealed_s2r", b.cnt.concealedS2R.Load()).
		Uint64("concealed_r2s", b.cnt.concealedR2S.Load()).
		Int("jitter_sip_frames", b.jSIP.Depth()).
		Int("jitter_rtc_frames", b.jRTC.Depth()).
		Uint64("jitter_sip_lost", sipLost).
		Uint64("jitter_sip_late", sipLate).
		Uint64("jitter_rtc_lost", rtcLost).
		Uint64("jitter_rtc_late", rtcLate).
		Uint64("write_us", b.writeUS.Load())
	if ca := b.connectedAt.Load(); ca != 0 {
		ev = ev.Int64("since_connect_ms", now.Sub(time.Unix(0, ca)).Milliseconds())
	}
	ev.Msg("media: trace")
}
