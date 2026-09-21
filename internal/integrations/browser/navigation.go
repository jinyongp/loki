package browser

import (
	"context"
	"time"
)

func (d *Driver) finishSessionNavigation(startGeneration uint64, performed bool, navigation string, result map[string]any, err error) (map[string]any, error) {
	if err != nil {
		return nil, err
	}
	if performed {
		if d.generation == startGeneration {
			d.generation++
		}
		d.stateGeneration++
	}
	if result == nil {
		result = map[string]any{}
	}
	result["navigation"] = navigation
	result["performed"] = performed
	result["active_tab_id"] = shortID(d.target)
	result["browser_generation"] = d.generation
	return result, nil
}

func (d *Driver) historyNavigation(ctx context.Context, direction int) (map[string]any, bool, error) {
	var history struct {
		CurrentIndex int
		Entries      []struct {
			ID  int
			URL string
		}
	}
	if err := d.client.Call(ctx, d.sessions[d.target], "Page.getNavigationHistory", nil, &history); err != nil {
		return nil, false, err
	}
	targetIndex := history.CurrentIndex + direction
	if targetIndex < 0 || targetIndex >= len(history.Entries) {
		page, err := d.page(ctx)
		return page, false, err
	}
	target := history.Entries[targetIndex]
	d.generation++
	if err := d.client.Call(ctx, d.sessions[d.target], "Page.navigateToHistoryEntry", map[string]any{"entryId": target.ID}, nil); err != nil {
		return nil, true, err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		page, err := d.page(ctx)
		if err == nil && page["url"] == target.URL {
			break
		}
		select {
		case <-ctx.Done():
			return nil, true, ctx.Err()
		case <-ticker.C:
		}
	}
	if err := d.waitPage(ctx, ""); err != nil {
		return nil, true, err
	}
	page, err := d.page(ctx)
	return page, true, err
}

func (d *Driver) currentLoader(ctx context.Context) (string, error) {
	var tree struct {
		FrameTree struct {
			Frame struct {
				LoaderID string
			}
		}
	}
	if err := d.client.Call(ctx, d.sessions[d.target], "Page.getFrameTree", nil, &tree); err != nil {
		return "", err
	}
	return tree.FrameTree.Frame.LoaderID, nil
}

func (d *Driver) waitLoaderChange(ctx context.Context, previous string) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := d.currentLoader(ctx)
		if err != nil {
			return err
		}
		if current != "" && current != previous {
			return d.waitPage(ctx, current)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (d *Driver) reloadPage(ctx context.Context) (map[string]any, error) {
	loader, err := d.currentLoader(ctx)
	if err != nil {
		return nil, err
	}
	d.generation++
	if err = d.client.Call(ctx, d.sessions[d.target], "Page.reload", nil, nil); err != nil {
		return nil, err
	}
	if err = d.waitLoaderChange(ctx, loader); err != nil {
		return nil, err
	}
	return d.page(ctx)
}

func (d *Driver) stopLoading(ctx context.Context) (map[string]any, error) {
	d.generation++
	if err := d.client.Call(ctx, d.sessions[d.target], "Page.stopLoading", nil, nil); err != nil {
		return nil, err
	}
	return d.page(ctx)
}
