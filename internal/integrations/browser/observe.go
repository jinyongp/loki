package browser

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"
)

func optionalText(args map[string]any, key string, limit int) (string, error) {
	if args[key] == nil {
		return "", nil
	}
	s, ok := args[key].(string)
	if !ok || utf8.RuneCountInString(s) > limit {
		return "", errors.New(key + " must be a bounded string")
	}
	return s, nil
}
func (d *Driver) observe(ctx context.Context, operation string, args map[string]any) (map[string]any, error) {
	if operation == "request" {
		return d.request(ctx, args)
	}
	since, err := integer(args, "since_sequence", 0, 0, 2147483647)
	if err != nil {
		return nil, err
	}
	defaultLimit, maxLimit := 100, 500
	if operation == "debug_diagnostics" {
		defaultLimit, maxLimit = 50, 200
	}
	limit, err := integer(args, "limit", defaultLimit, 1, maxLimit)
	if err != nil {
		return nil, err
	}
	switch operation {
	case "console", "websockets", "page_errors":
		level, err := optionalText(args, "level", 50)
		if err != nil {
			return nil, err
		}
		return d.debug.Events(operation, level, int64(since), limit), nil
	case "network":
		var status *int
		if args["status_min"] != nil {
			n, err := integer(args, "status_min", 100, 100, 599)
			if err != nil {
				return nil, err
			}
			status = &n
		}
		resource, err := optionalText(args, "resource_type", 50)
		if err != nil {
			return nil, err
		}
		return d.debug.Network(status, args["failed_only"] == true, resource, int64(since), limit), nil
	case "debug_diagnostics":
		page, err := d.page(ctx)
		if err != nil {
			return nil, err
		}
		result := d.debug.Diagnostics(int64(since), limit)
		result["page"] = page
		return result, nil
	}
	return nil, errors.New("unknown browser observation")
}
func (d *Driver) request(ctx context.Context, args map[string]any) (map[string]any, error) {
	id, err := optionalText(args, "request_id", 300)
	if err != nil || id == "" {
		return nil, errors.New("request_id must be a non-empty string returned by browser_network")
	}
	detail := d.debug.Request(id)
	if detail == nil {
		return nil, errors.New("request_id is not retained; call browser_network for current ids")
	}
	if args["include_body"] != true {
		return detail, nil
	}
	maximum, err := integer(args, "max_body_chars", 65536, 1, 262144)
	if err != nil {
		return nil, err
	}
	mime := strings.ToLower(text(detail["mime_type"], 500))
	textLike := strings.HasPrefix(mime, "text/")
	for _, marker := range []string{"json", "javascript", "xml", "svg", "x-www-form-urlencoded"} {
		textLike = textLike || strings.Contains(mime, marker)
	}
	if !textLike {
		return nil, errors.New("response body is available only for retained text-like responses")
	}
	encoded, ok := detail["encoded_bytes"].(float64)
	if !ok || encoded > 1048576 || detail["finished"] != true {
		return nil, errors.New("response body exceeds the 1 MiB retrieval limit or has not finished")
	}
	session, _ := detail["session_id"].(string)
	if session == "" {
		session = d.sessions[d.target]
	}
	var response struct {
		Body          string
		Base64Encoded bool
	}
	if err = d.client.Call(ctx, session, "Network.getResponseBody", map[string]any{"requestId": id}, &response); err != nil {
		return nil, errors.New("response body is no longer available")
	}
	body := response.Body
	if response.Base64Encoded {
		if len(body) > base64.StdEncoding.EncodedLen(1048576) {
			return nil, errors.New("decoded response body exceeds the 1 MiB retrieval limit")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(body)
		if err != nil {
			return nil, errors.New("response body encoding is invalid")
		}
		body = strings.ToValidUTF8(string(data), "�")
	}
	// Compressed transfer size is insufficient: bound the decoded body as well.
	if len(body) > 1048576 {
		return nil, errors.New("decoded response body exceeds the 1 MiB retrieval limit")
	}
	detail["body"] = cut(body, maximum)
	detail["body_truncated"] = utf8.RuneCountInString(body) > maximum
	return detail, nil
}
