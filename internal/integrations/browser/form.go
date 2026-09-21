package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

type optionSelector struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
	Index int    `json:"index,omitempty"`
}

func parseOptionSelectors(args map[string]any) ([]optionSelector, error) {
	raw, ok := args["options"].([]any)
	if !ok || len(raw) == 0 || len(raw) > 50 {
		return nil, errors.New("options must contain between 1 and 50 option selectors")
	}
	result := make([]optionSelector, 0, len(raw))
	for _, item := range raw {
		value, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("each option selector must be an object")
		}
		fields := 0
		selector := optionSelector{}
		if text, exists := value["value"]; exists {
			s, ok := text.(string)
			if !ok || utf8.RuneCountInString(s) > 500 {
				return nil, errors.New("option value must be a bounded string")
			}
			fields++
			selector.Kind, selector.Value = "value", s
		}
		if text, exists := value["label"]; exists {
			s, ok := text.(string)
			if !ok || utf8.RuneCountInString(s) > 500 {
				return nil, errors.New("option label must be a bounded string")
			}
			fields++
			selector.Kind, selector.Value = "label", s
		}
		if _, exists := value["index"]; exists {
			n, err := integer(value, "index", -1, 0, 100000)
			if err != nil {
				return nil, err
			}
			fields++
			selector.Kind, selector.Index = "index", n
		}
		if fields != 1 {
			return nil, errors.New("each option selector must specify exactly one of value, label, or index")
		}
		result = append(result, selector)
	}
	return result, nil
}

func (d *Driver) focusElement(ctx context.Context, args map[string]any) (map[string]any, error) {
	index, err := integer(args, "index", -1, 0, 1000000)
	if err != nil || index < 0 {
		return nil, errors.New("index must identify an element from browser_observe action=state")
	}
	if _, err = d.element(ctx, index, true); err != nil {
		return nil, err
	}
	script := fmt.Sprintf(`(() => {
 const element=globalThis.__lokiNodes?.[%d];
 if (!element || !element.isConnected) throw new Error('stale element');
 element.focus({preventScroll:true});
 return element.ownerDocument.activeElement===element || element.getRootNode().activeElement===element;
})()`, index)
	var focused bool
	if err = d.evaluate(ctx, script, &focused); err != nil {
		return nil, err
	}
	if !focused {
		return nil, errors.New("element could not be focused")
	}
	return map[string]any{"focused": true, "index": index}, nil
}

func (d *Driver) setChecked(ctx context.Context, args map[string]any) (map[string]any, error) {
	index, err := integer(args, "index", -1, 0, 1000000)
	if err != nil || index < 0 {
		return nil, errors.New("index must identify a checkbox or radio from browser_observe action=state")
	}
	checked, ok := args["checked"].(bool)
	if !ok {
		return nil, errors.New("checked must be a boolean")
	}
	if _, err = d.element(ctx, index, true); err != nil {
		return nil, err
	}
	script := fmt.Sprintf(`(() => {
 const element=globalThis.__lokiNodes?.[%d];
 if (!element || !element.isConnected) return {ok:false};
 if (element.tagName!=='INPUT' || (element.type!=='checkbox' && element.type!=='radio')) return {ok:false};
 const changed=element.checked!==%t;
 if (changed) {
   element.checked=%t;
   element.dispatchEvent(new Event('input',{bubbles:true}));
   element.dispatchEvent(new Event('change',{bubbles:true}));
 }
 return {ok:true,checked:element.checked,changed};
})()`, index, checked, checked)
	var result map[string]any
	if err = d.evaluate(ctx, script, &result); err != nil {
		return nil, err
	}
	if result["ok"] != true {
		return nil, errors.New("element is not a native checkbox or radio")
	}
	return map[string]any{
		"index": index, "checked": result["checked"], "changed": result["changed"],
	}, nil
}

func (d *Driver) selectOptions(ctx context.Context, args map[string]any) (map[string]any, error) {
	index, err := integer(args, "index", -1, 0, 1000000)
	if err != nil || index < 0 {
		return nil, errors.New("index must identify a select element from browser_observe action=state")
	}
	selectors, err := parseOptionSelectors(args)
	if err != nil {
		return nil, err
	}
	if _, err = d.element(ctx, index, true); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(selectors)
	if err != nil {
		return nil, err
	}
	script := fmt.Sprintf(`(() => {
 const element=globalThis.__lokiNodes?.[%d],selectors=%s;
 if (!element || !element.isConnected || element.tagName!=='SELECT') return {ok:false,error:'not_select'};
 const options=Array.from(element.options),resolved=[];
 for (const selector of selectors) {
   let matches=[];
   if (selector.kind==='index') {
     if (selector.index>=0 && selector.index<options.length) matches=[selector.index];
   } else if (selector.kind==='value') {
     options.forEach((option,index)=>{if(option.value===selector.value)matches.push(index)});
   } else if (selector.kind==='label') {
     options.forEach((option,index)=>{if(option.label===selector.value)matches.push(index)});
   }
   if (matches.length!==1) return {ok:false,error:matches.length?'ambiguous':'missing'};
   resolved.push(matches[0]);
 }
 const unique=Array.from(new Set(resolved));
 if (!element.multiple && unique.length!==1) return {ok:false,error:'single_multiple'};
 const selected=new Set(unique),before=options.map(option=>option.selected);
 options.forEach((option,index)=>option.selected=selected.has(index));
 const changed=options.some((option,index)=>option.selected!==before[index]);
 if (changed) {
   element.dispatchEvent(new Event('input',{bubbles:true}));
   element.dispatchEvent(new Event('change',{bubbles:true}));
 }
 return {ok:true,multiple:element.multiple,changed,selected:unique.map(index=>({
   index,value:options[index].value,label:options[index].label
 }))};
})()`, index, string(encoded))
	var result map[string]any
	if err = d.evaluate(ctx, script, &result); err != nil {
		return nil, err
	}
	if result["ok"] != true {
		switch result["error"] {
		case "not_select":
			return nil, errors.New("element is not a select")
		case "ambiguous":
			return nil, errors.New("option selector is ambiguous")
		case "missing":
			return nil, errors.New("option selector did not match")
		case "single_multiple":
			return nil, errors.New("single-select elements require exactly one unique option")
		default:
			return nil, errors.New("select option operation failed")
		}
	}
	return map[string]any{
		"index": index, "multiple": result["multiple"], "changed": result["changed"], "selected": result["selected"],
	}, nil
}
