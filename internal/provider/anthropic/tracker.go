package anthropic

import (
	"encoding/json"
	"strings"
)

type streamTracker struct {
	buf       string
	open      []int
	maxIndex  int
	sawEvents bool
}

func newStreamTracker() *streamTracker {
	return &streamTracker{maxIndex: -1}
}

func (t *streamTracker) Feed(chunk string) {
	t.buf += chunk
	for {
		idx := strings.Index(t.buf, "\n\n")
		if idx < 0 {
			return
		}
		frame := t.buf[:idx]
		t.buf = t.buf[idx+2:]
		if strings.TrimSpace(frame) == "" {
			continue
		}
		t.observe(frame)
	}
}

func (t *streamTracker) observe(frame string) {
	event := ""
	var dataParts []string
	for _, line := range strings.Split(frame, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataParts = append(dataParts, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if event == "" && len(dataParts) == 0 {
		return
	}
	t.sawEvents = true
	var data map[string]any
	if err := json.Unmarshal([]byte(strings.Join(dataParts, "\n")), &data); err != nil {
		return
	}
	idx, ok := eventIndex(data)
	if !ok {
		return
	}
	switch event {
	case "content_block_start":
		if idx > t.maxIndex {
			t.maxIndex = idx
		}
		t.open = append(t.open, idx)
	case "content_block_stop":
		t.removeOpen(idx)
	}
}

func (t *streamTracker) removeOpen(idx int) {
	for i := len(t.open) - 1; i >= 0; i-- {
		if t.open[i] == idx {
			t.open = append(t.open[:i], t.open[i+1:]...)
			return
		}
	}
}

func (t *streamTracker) PopOpen() (int, bool) {
	if len(t.open) == 0 {
		return 0, false
	}
	idx := t.open[len(t.open)-1]
	t.open = t.open[:len(t.open)-1]
	return idx, true
}

func (t *streamTracker) NextIndex() int  { return t.maxIndex + 1 }
func (t *streamTracker) SawEvents() bool { return t.sawEvents }

func eventIndex(data map[string]any) (int, bool) {
	value, ok := data["index"]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}
