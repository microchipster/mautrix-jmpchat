#!/usr/bin/env python3
"""Latency patch v2 for mrtc-sip-bridge pkg/media/jitter.go (F2 + F3).

F1 (bridge.go: JitterDelay 25 ms / JitterMax 120 ms) was already applied.
"""
import re
from pathlib import Path

J = Path('/tmp/opencode/mrtc-sip-bridge-patch/pkg/media/jitter.go')
src = J.read_text()

# F2: kill the 20 ms/s latency ratchet (drop one frame after 3-slot slack, 50 consecutive pops).
a = 'latencySlack = 3'
b = 'latencyPops  = 50'
assert src.count(a) == 1, ('latencySlack literal count', src.count(a))
assert src.count(b) == 1, ('latencyPops literal count', src.count(b))
src = src.replace(a, 'latencySlack = 1')
src = src.replace(b, 'latencyPops  = 5')

# F3: never fall back to multi-tick re-buffering after underruns; the store path resyncs next on
# the next arriving packet while started, so speech resumes immediately.
new = '''	if j.count == 0 {
		// Underrun: do not advance next, so a merely-late packet still plays (a DTX-style gap
		// resumes at the packet's own seq). Keep playing state so speech resumes without the
		// multi-frame re-buffering pause.
		j.underruns++
		return nil, false
	}'''

pat = re.compile(
    r'\tif j\.count == 0 \{\n'
    r'(?:\t\t//[^\n]*\n)*'
    r'\t\tj\.underruns\+\+\n'
    r'\t\tif j\.underruns >= 10 \{\n'
    r'\t\t\tj\.started, j\.underruns = false, 0\n'
    r'\t\t\}\n'
    r'\t\treturn nil, false\n'
    r'\t\}'
)
matches = pat.findall(src)
assert len(matches) == 1, ('underrun block regex matches', len(matches))
src = pat.sub(lambda m: new, src)

J.write_text(src)
print('patched jitter.go OK (F2 + F3)')
