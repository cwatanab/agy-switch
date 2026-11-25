package quota

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultAICodeURL           = "https://aicode.googleapis.com"
	aicodeFetchQuotaStatusPath = "/google.gca.aicode.v1alpha.PredictionService/FetchQuotaStatus"
)

type aiCodeQuotaSummary struct {
	Description string
	Groups      []aiCodeQuotaGroup
}

type aiCodeQuotaGroup struct {
	DisplayName string
	Description string
	Buckets     []aiCodeQuotaBucket
}

type aiCodeQuotaBucket struct {
	BucketID           string
	DisplayName        string
	Description        string
	RefreshWindowLabel string
	RemainingFraction  float64
	Disabled           bool
	ResetTime          string
}

func (c *Client) fetchAICodeQuota(ctx context.Context, accessToken string) (*aiCodeQuotaSummary, error) {
	if c.aicodeURL == "" {
		return nil, fmt.Errorf("AICode endpoint disabled")
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		summary, retry, err := c.fetchAICodeQuotaOnce(ctx, accessToken)
		if err == nil {
			return summary, nil
		}
		lastErr = err
		if !retry || attempt == 1 || ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func (c *Client) fetchAICodeQuotaOnce(ctx context.Context, accessToken string) (*aiCodeQuotaSummary, bool, error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	defer func() { <-c.slots }()

	grpcFrame := []byte{0, 0, 0, 0, 0}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.aicodeURL, "/")+aicodeFetchQuotaStatusPath, bytes.NewReader(grpcFrame))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	req.Header.Set("Grpc-Accept-Encoding", "identity,gzip")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "antigravity/1.2.9")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		retry := !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
		return nil, retry, fmt.Errorf("AICode request failed: %w", err)
	}
	defer resp.Body.Close()

	const maxResponse = 4 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, true, fmt.Errorf("read AICode response: %w", err)
	}
	if len(body) > maxResponse {
		return nil, false, fmt.Errorf("AICode response exceeds size limit")
	}
	if resp.StatusCode != http.StatusOK {
		s := resp.StatusCode
		retry := s == 429 || s == 500 || s == 502 || s == 503 || s == 504
		return nil, retry, httpStatusError{status: s}
	}

	grpcStatus := resp.Trailer.Get("Grpc-Status")
	if grpcStatus == "" {
		grpcStatus = resp.Header.Get("Grpc-Status")
	}
	if grpcStatus != "" && grpcStatus != "0" {
		grpcMsg := resp.Trailer.Get("Grpc-Message")
		if grpcMsg == "" {
			grpcMsg = resp.Header.Get("Grpc-Message")
		}
		retry := grpcStatus == "4" || grpcStatus == "14" // DEADLINE_EXCEEDED / UNAVAILABLE
		if grpcMsg != "" {
			return nil, retry, fmt.Errorf("AICode gRPC status %s: %s", grpcStatus, grpcMsg)
		}
		return nil, retry, fmt.Errorf("AICode gRPC status %s", grpcStatus)
	}

	summary, err := decodeAICodeFetchQuotaStatus(body)
	return summary, false, err
}

func decodeAICodeFetchQuotaStatus(payload []byte) (*aiCodeQuotaSummary, error) {
	if len(payload) < 5 {
		return nil, fmt.Errorf("truncated gRPC frame (%d bytes)", len(payload))
	}
	compressed := payload[0]
	msgLen := binary.BigEndian.Uint32(payload[1:5])
	if uint64(len(payload)-5) < uint64(msgLen) {
		return nil, fmt.Errorf("incomplete gRPC message: have %d, want %d", len(payload)-5, msgLen)
	}
	msg := payload[5 : 5+msgLen]
	if compressed == 1 {
		zr, err := gzip.NewReader(bytes.NewReader(msg))
		if err != nil {
			return nil, fmt.Errorf("gzip gRPC frame: %w", err)
		}
		decompressed, err := io.ReadAll(io.LimitReader(zr, 4*1024*1024))
		_ = zr.Close()
		if err != nil {
			return nil, fmt.Errorf("decompress gRPC frame: %w", err)
		}
		msg = decompressed
	} else if compressed != 0 {
		return nil, fmt.Errorf("unsupported gRPC compression flag %d", compressed)
	}

	var summary *aiCodeQuotaSummary
	if err := walkProtoFields(msg, func(fieldNum uint64, wireType uint8, data []byte, varintVal uint64, fixed32 uint32) error {
		if fieldNum == 2 && wireType == 2 {
			s, err := decodeAICodeQuotaSummary(data)
			if err != nil {
				return err
			}
			summary = s
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if summary == nil {
		return &aiCodeQuotaSummary{}, nil
	}
	return summary, nil
}

func decodeAICodeQuotaSummary(data []byte) (*aiCodeQuotaSummary, error) {
	summary := &aiCodeQuotaSummary{}
	err := walkProtoFields(data, func(fieldNum uint64, wireType uint8, raw []byte, _ uint64, _ uint32) error {
		switch {
		case fieldNum == 1 && wireType == 2:
			summary.Description = string(raw)
		case fieldNum == 2 && wireType == 2:
			g, err := decodeAICodeQuotaGroup(raw)
			if err != nil {
				return err
			}
			summary.Groups = append(summary.Groups, g)
		}
		return nil
	})
	return summary, err
}

func decodeAICodeQuotaGroup(data []byte) (aiCodeQuotaGroup, error) {
	var group aiCodeQuotaGroup
	err := walkProtoFields(data, func(fieldNum uint64, wireType uint8, raw []byte, _ uint64, _ uint32) error {
		switch {
		case fieldNum == 1 && wireType == 2:
			group.DisplayName = string(raw)
		case fieldNum == 2 && wireType == 2:
			group.Description = string(raw)
		case fieldNum == 3 && wireType == 2:
			b, err := decodeAICodeQuotaBucket(raw)
			if err != nil {
				return err
			}
			group.Buckets = append(group.Buckets, b)
		}
		return nil
	})
	return group, err
}

func decodeAICodeQuotaBucket(data []byte) (aiCodeQuotaBucket, error) {
	var bucket aiCodeQuotaBucket
	err := walkProtoFields(data, func(fieldNum uint64, wireType uint8, raw []byte, varintVal uint64, fixed32 uint32) error {
		switch {
		case fieldNum == 1 && wireType == 2:
			bucket.BucketID = string(raw)
		case fieldNum == 2 && wireType == 2:
			bucket.DisplayName = string(raw)
		case fieldNum == 3 && wireType == 2:
			bucket.Description = string(raw)
		case fieldNum == 4 && wireType == 2:
			bucket.RefreshWindowLabel = string(raw)
		case fieldNum == 5 && wireType == 5:
			bucket.RemainingFraction = float64(math.Float32frombits(fixed32))
		case fieldNum == 6 && wireType == 0:
			bucket.Disabled = varintVal != 0
		case fieldNum == 7 && wireType == 2:
			ts, err := decodeProtoTimestamp(raw)
			if err != nil {
				return err
			}
			bucket.ResetTime = ts
		}
		return nil
	})
	return bucket, err
}

func decodeProtoTimestamp(data []byte) (string, error) {
	var secs int64
	var nanos int32
	err := walkProtoFields(data, func(fieldNum uint64, wireType uint8, _ []byte, varintVal uint64, _ uint32) error {
		if wireType != 0 {
			return nil
		}
		switch fieldNum {
		case 1:
			secs = int64(varintVal)
		case 2:
			nanos = int32(varintVal)
		}
		return nil
	})
	if err != nil || secs <= 0 {
		return "", err
	}
	return time.Unix(secs, int64(nanos)).UTC().Format(time.RFC3339), nil
}

func walkProtoFields(data []byte, fn func(fieldNum uint64, wireType uint8, raw []byte, varintVal uint64, fixed32 uint32) error) error {
	i := 0
	for i < len(data) {
		tag, n := binary.Uvarint(data[i:])
		if n <= 0 {
			return fmt.Errorf("invalid protobuf tag")
		}
		i += n
		fieldNum := tag >> 3
		wireType := uint8(tag & 7)
		switch wireType {
		case 0: // varint
			v, vn := binary.Uvarint(data[i:])
			if vn <= 0 {
				return fmt.Errorf("invalid protobuf varint")
			}
			i += vn
			if err := fn(fieldNum, wireType, nil, v, 0); err != nil {
				return err
			}
		case 1: // 64-bit
			if i+8 > len(data) {
				return fmt.Errorf("truncated 64-bit protobuf field")
			}
			i += 8
		case 2: // length-delimited
			ln, lnBytes := binary.Uvarint(data[i:])
			if lnBytes <= 0 {
				return fmt.Errorf("invalid protobuf length")
			}
			i += lnBytes
			if ln > uint64(len(data)-i) {
				return fmt.Errorf("truncated length-delimited protobuf field")
			}
			chunk := data[i : i+int(ln)]
			i += int(ln)
			if err := fn(fieldNum, wireType, chunk, 0, 0); err != nil {
				return err
			}
		case 5: // 32-bit
			if i+4 > len(data) {
				return fmt.Errorf("truncated 32-bit protobuf field")
			}
			v := binary.LittleEndian.Uint32(data[i : i+4])
			i += 4
			if err := fn(fieldNum, wireType, nil, 0, v); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported protobuf wire type %d", wireType)
		}
	}
	return nil
}

func applyAICodeSummary(info *QuotaInfo, summary *aiCodeQuotaSummary) {
	if summary == nil {
		return
	}
	for _, g := range summary.Groups {
		gName := strings.ToLower(g.DisplayName)
		for _, b := range g.Buckets {
			id := strings.ToLower(b.BucketID)
			window := strings.ToLower(b.RefreshWindowLabel)
			if window == "" {
				if strings.HasSuffix(id, "-5h") {
					window = "5h"
				} else if strings.HasSuffix(id, "-weekly") {
					window = "weekly"
				}
			}
			isGemini := strings.Contains(gName, "gemini") || strings.HasPrefix(id, "gemini-")
			isClaude := strings.Contains(gName, "claude") || strings.HasPrefix(id, "3p-")
			disabled := b.Disabled || (window == "5h" && strings.Contains(strings.ToLower(b.Description), "does not currently apply"))

			if isGemini {
				if window == "5h" {
					info.Gemini5hFraction = b.RemainingFraction
					info.Gemini5hReset = b.ResetTime
					info.Gemini5hDisabled = disabled
					info.HasGemini = true
				} else if window == "weekly" {
					info.GeminiWeeklyFraction = b.RemainingFraction
					info.GeminiWeeklyReset = b.ResetTime
					info.GeminiWeeklyDisabled = disabled
					info.HasGeminiWeekly = true
					if !info.HasClaudeWeekly {
						info.WeeklyFraction = b.RemainingFraction
						info.WeeklyReset = b.ResetTime
					}
				}
			} else if isClaude {
				if window == "5h" {
					info.Claude5hFraction = b.RemainingFraction
					info.Claude5hReset = b.ResetTime
					info.Claude5hDisabled = disabled
					info.HasClaude = true
				} else if window == "weekly" {
					info.ClaudeWeeklyFraction = b.RemainingFraction
					info.ClaudeWeeklyReset = b.ResetTime
					info.ClaudeWeeklyDisabled = disabled
					info.HasClaudeWeekly = true
					info.WeeklyFraction = b.RemainingFraction
					info.WeeklyReset = b.ResetTime
				}
			}
		}
	}
}
