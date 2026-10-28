#!/usr/bin/env python3
"""Apply latency patches (F1-F3) to the mrtc-sip-bridge clone."""
from pathlib import Path

base = Path('/tmp/opencode/mrtc-sip-bridge-patch/pkg/media')

b = base / 'bridge.go'
s = b.read_text()
assert 'c.JitterDelay = 60 * time.Millisecond' in s, 'JitterDelay default not found'
assert 'c.JitterMax = 400 * time.Millisecond' in s, 'JitterMax default not found'
s = s.replace('c.JitterDelay = 60 * time.Millisecond', 'c.JitterDelay = 25 * time.Millisecond')
s = s.replace('c.JitterMax = 400 * time.Millisecond', 'c.JitterMax = 120 * time.Millisecond')
b.write_text(s)
print('bridge.go patched: JitterDelay 25ms, JitterMax 120ms')

j = base / 'jitter.go'
s = j.read_text()
assert 'latencySlack = 3' in s, 'latencySlack not found'
assert 'latencyPops  = 50' in s, 'latencyPops not found'
s = s.replace('latencySlack = 3', 'latencySlack = 1')
s = s.replace('latencyPops  = 50', 'latencyPops  = 5')

old = """	if j.count == 0 {
		j.underruns++
		if j.underruns >= 10 {
			j.started, j.underruns = false, 0
		}
		return nil, false
	}"""
new = """	if j.count == 0 {
		j.underruns++
		// Stay started: the store path resyncs to the next packet when the buffer is empty, so a
		// DTX gap or a brief underrun resumes without re-buffering the full delay target.
		return nil, false
	}"""
assert s.count(old) == 1, f'underrun block found {s.count(old)} times'
s = s.replace(old, new)
j.write_text(s)
print('jitter.go patched: slack 1, pops 5, stay-started on underrun')
