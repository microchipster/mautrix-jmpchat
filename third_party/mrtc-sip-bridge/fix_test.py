#!/usr/bin/env python3
"""Update TestBridgeSIPToRTC expectations for the latency-first jitter delay."""
from pathlib import Path

p = Path('/tmp/opencode/mrtc-sip-bridge-patch/pkg/media/bridge_test.go')
t = p.read_text()
assert 'ConcealedSIPToRTC != 2' in t, 'expected literal not found'
t = t.replace('ConcealedSIPToRTC != 2', 'ConcealedSIPToRTC != 0', 1)
marker = '\tif st.SIPRx != 60'
assert marker in t, 'if-line marker not found'
t = t.replace(
    marker,
    '\t// 25 ms jitter delay ~= 1 frame: nothing is concealed while the buffer warms (was 2 at 60 ms).\n' + marker,
    1,
)
p.write_text(t)
print('patched', p)
