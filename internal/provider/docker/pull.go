package docker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/containerd/errdefs"
)

const (
	maxPullLines  = 100_000
	maxPullLine   = 64 << 10
	maxPullStream = 8 << 20
	maxPullLayers = 256

	pullEmitInterval = 400 * time.Millisecond
)

var pullNow = time.Now

// pullMessage is the only subset of a Docker pull line Meridian decodes.
// Status text is matched against a fixed allowlist and then discarded.
type pullMessage struct {
	Status string `json:"status"`
	ID     string `json:"id"`
	Detail *struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
	ErrorDetail *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errorDetail"`
	Error string `json:"error"`
}

type pullLayer struct {
	downCur, downTot int64
	extCur, extTot   int64
	downSeen         bool
	extSeen          bool
}

type pullAggregator struct {
	layers       map[string]*pullLayer
	order        []string
	downloadSeen bool
	extractSeen  bool
}

func (a *pullAggregator) observe(message pullMessage) (current, total int64, ok bool) {
	if message.ErrorDetail != nil || message.Error != "" {
		return 0, 0, false
	}
	kind, known := pullStatusKind(message.Status)
	if !known {
		return 0, 0, false
	}
	id, valid := pullLayerID(message.ID)
	if !valid {
		return 0, 0, false
	}
	layer := a.layer(id)
	if layer == nil {
		current, total = a.snapshot()
		return current, total, true
	}
	switch kind {
	case pullDownload:
		cur, tot, accept := boundedProgress(message)
		if !accept {
			current, total = a.snapshot()
			return current, total, true
		}
		if !layer.downSeen || cur >= layer.downCur {
			layer.downCur = cur
		}
		if tot > 0 {
			layer.downTot = tot
			if layer.downCur > layer.downTot {
				layer.downCur = layer.downTot
			}
		}
		layer.downSeen = true
		a.downloadSeen = true
	case pullDownloadDone:
		if layer.downTot > 0 {
			layer.downCur = layer.downTot
		}
		layer.downSeen = true
		a.downloadSeen = true
	case pullExtract:
		cur, tot, accept := boundedProgress(message)
		if !accept {
			current, total = a.snapshot()
			return current, total, true
		}
		if !layer.extSeen || cur >= layer.extCur {
			layer.extCur = cur
		}
		if tot > 0 {
			layer.extTot = tot
			if layer.extCur > layer.extTot {
				layer.extCur = layer.extTot
			}
		}
		layer.extSeen = true
		a.extractSeen = true
	case pullExtractDone:
		if layer.extTot > 0 {
			layer.extCur = layer.extTot
		}
		layer.extSeen = true
		a.extractSeen = true
	}
	current, total = a.snapshot()
	return current, total, true
}

func (a *pullAggregator) layer(id string) *pullLayer {
	if a.layers == nil {
		a.layers = make(map[string]*pullLayer)
	}
	if layer, ok := a.layers[id]; ok {
		return layer
	}
	if len(a.layers) >= maxPullLayers {
		return nil
	}
	layer := &pullLayer{}
	a.layers[id] = layer
	a.order = append(a.order, id)
	return layer
}

func (a *pullAggregator) snapshot() (int64, int64) {
	useExtract := !a.downloadSeen && a.extractSeen
	var current int64
	total := int64(0)
	complete := true
	seen := false
	for _, id := range a.order {
		layer := a.layers[id]
		var cur, tot int64
		var active bool
		if useExtract {
			active = layer.extSeen
			cur, tot = layer.extCur, layer.extTot
		} else {
			active = layer.downSeen
			cur, tot = layer.downCur, layer.downTot
		}
		if !active {
			continue
		}
		seen = true
		current += cur
		if tot <= 0 {
			complete = false
			continue
		}
		total += tot
	}
	if !seen || !complete {
		return current, 0
	}
	return current, total
}

type pullKind int

const (
	pullDownload pullKind = iota
	pullDownloadDone
	pullExtract
	pullExtractDone
)

func pullStatusKind(status string) (pullKind, bool) {
	switch status {
	case "Downloading":
		return pullDownload, true
	case "Download complete":
		return pullDownloadDone, true
	case "Extracting":
		return pullExtract, true
	case "Pull complete":
		return pullExtractDone, true
	default:
		return 0, false
	}
}

func pullLayerID(id string) (string, bool) {
	if id == "" || len(id) > 128 {
		return "", false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') &&
			r != ':' && r != '.' && r != '_' && r != '-' {
			return "", false
		}
	}
	return id, true
}

func boundedProgress(message pullMessage) (int64, int64, bool) {
	if message.Detail == nil {
		return 0, 0, false
	}
	current, total := message.Detail.Current, message.Detail.Total
	if current < 0 || total < 0 || current > domain.MaxPullBytes || total > domain.MaxPullBytes {
		return 0, 0, false
	}
	return current, total, true
}

func classifyDockerPull(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errdefs.IsDeadlineExceeded(err) || errdefs.IsUnavailable(err) {
		return &domain.StartupFailure{Code: domain.PullRegistryUnreachable}
	}
	if errdefs.IsUnauthorized(err) || errdefs.IsPermissionDenied(err) {
		return &domain.StartupFailure{Code: domain.PullAuthRequired}
	}
	if errdefs.IsNotFound(err) {
		return &domain.StartupFailure{Code: domain.PullImageNotFound}
	}
	return &domain.StartupFailure{Code: domain.ClassifyPullText(err.Error())}
}

func classifyPullMessage(message pullMessage) error {
	text := message.Error
	code := 0
	if message.ErrorDetail != nil {
		if message.ErrorDetail.Message != "" {
			text = message.ErrorDetail.Message
		}
		code = message.ErrorDetail.Code
	}
	if text == "" && code == 0 {
		return nil
	}
	failure := domain.ClassifyPullText(text)
	if failure == "" {
		switch code {
		case 401, 403:
			failure = domain.PullAuthRequired
		case 404:
			failure = domain.PullImageNotFound
		case 408, 429, 500, 502, 503, 504:
			failure = domain.PullRegistryUnreachable
		}
	}
	return &domain.StartupFailure{Code: failure}
}

func shouldEmitPull(now, last time.Time, prevCurrent, prevTotal, current, total int64, emitted bool) bool {
	if !emitted {
		return true
	}
	if current == prevCurrent && total == prevTotal {
		return false
	}
	return last.IsZero() || now.Sub(last) >= pullEmitInterval
}

func readPullLine(reader *bufio.Reader, read *int) ([]byte, error) {
	var buf bytes.Buffer
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			if buf.Len()+len(chunk) > maxPullLine || *read+buf.Len()+len(chunk) > maxPullStream {
				if errors.Is(err, bufio.ErrBufferFull) {
					for {
						_, discardErr := reader.ReadSlice('\n')
						if !errors.Is(discardErr, bufio.ErrBufferFull) {
							break
						}
					}
				}
				return nil, errPullOversized
			}
			buf.Write(chunk)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if buf.Len() == 0 && errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		*read += buf.Len()
		line := bytes.TrimSpace(buf.Bytes())
		if len(line) == 0 && err != nil {
			return nil, err
		}
		return line, err
	}
}

var errPullOversized = errors.New("pull progress line exceeds the size bound")
