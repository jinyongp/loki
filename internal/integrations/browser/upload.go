package browser

import (
	"context"
	"errors"
	"fmt"
)

func (d *Driver) elementObjectID(ctx context.Context, index int) (string, error) {
	if _, err := d.element(ctx, index, true); err != nil {
		return "", err
	}
	check := fmt.Sprintf(`(() => {
 const element=globalThis.__lokiNodes?.[%d];
 return !!element && element.isConnected && element.tagName==='INPUT' && element.type==='file' && !element.disabled;
})()`, index)
	var valid bool
	if err := d.evaluate(ctx, check, &valid); err != nil {
		return "", err
	}
	if !valid {
		return "", errors.New("element is not an enabled file input")
	}
	contextID, err := d.isolatedContext(ctx)
	if err != nil {
		return "", err
	}
	var result struct {
		Result struct {
			ObjectID string `json:"objectId"`
		}
		ExceptionDetails any `json:"exceptionDetails"`
	}
	if err = d.client.Call(ctx, d.sessions[d.target], "Runtime.evaluate", map[string]any{
		"expression":    fmt.Sprintf("globalThis.__lokiNodes?.[%d]", index),
		"contextId":     contextID,
		"returnByValue": false,
		"awaitPromise":  true,
		"timeout":       10000,
	}, &result); err != nil {
		return "", err
	}
	if result.ExceptionDetails != nil || result.Result.ObjectID == "" {
		return "", errors.New("file input object is unavailable; refresh browser state")
	}
	return result.Result.ObjectID, nil
}

func (d *Driver) uploadFiles(ctx context.Context, args map[string]any) (map[string]any, error) {
	index, err := integer(args, "index", -1, 0, 1000000)
	if err != nil || index < 0 {
		return nil, errors.New("index must identify a file input from browser_observe action=state")
	}
	refs, err := stagedUploadRefs(args)
	if err != nil {
		return nil, err
	}
	if d.uploads == nil {
		return nil, errors.New("browser file upload is not configured")
	}
	objectID, err := d.elementObjectID(ctx, index)
	if err != nil {
		return nil, err
	}
	prepared, err := d.uploads.Prepare(refs)
	if err != nil {
		return nil, err
	}
	if err = d.client.Call(ctx, d.sessions[d.target], "DOM.setFileInputFiles", map[string]any{
		"files":    prepared.paths,
		"objectId": objectID,
	}, nil); err != nil {
		d.uploads.Discard(prepared)
		return nil, err
	}
	d.uploads.Commit(prepared)
	return map[string]any{
		"uploaded":    true,
		"index":       index,
		"file_count":  len(prepared.paths),
		"total_bytes": prepared.bytes,
	}, nil
}
