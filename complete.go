package main

type Completion struct {
	class,
	name,
	typ string
}

type CompletionResponse struct {
	partial     int
	completions []Completion
}

func (k *kernelWrapper) handleCompleteRequest(receipt msgReceipt) error {
	// Extract the data from the request.
	reqContent := receipt.Msg.Content.(map[string]interface{})
	code := reqContent["code"].(string)
	cursorPos := int(reqContent["cursor_pos"].(float64))

	// autocomplete the code at the cursor position
	matches := k.FindMatch(code)

	// prepare the reply
	content := make(map[string]interface{})

	if len(matches) == 0 {
		content["ename"] = "ERROR"
		content["evalue"] = "no completions found"
		content["traceback"] = nil
		content["status"] = "error"
	} else {
		partialWord := ""
		content["cursor_start"] = float64(0 - len(partialWord))
		content["cursor_end"] = float64(cursorPos)
		content["matches"] = matches
		content["status"] = "ok"
	}

	return receipt.Reply("complete_reply", content)
}
