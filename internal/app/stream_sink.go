package app

type InferenceStreamSink struct {
	Text            func(string) error
	Reasoning       func(string) error
	ReasoningWarmup func() error
	KeepAlive       func() error
}

func (s InferenceStreamSink) EmitText(delta string) error {
	if delta == "" || s.Text == nil {
		return nil
	}
	return s.Text(delta)
}

func (s InferenceStreamSink) EmitReasoning(delta string) error {
	if delta == "" || s.Reasoning == nil {
		return nil
	}
	return s.Reasoning(delta)
}

func (s InferenceStreamSink) EmitReasoningWarmup() error {
	if s.ReasoningWarmup == nil {
		return nil
	}
	return s.ReasoningWarmup()
}

func (s InferenceStreamSink) EmitKeepAlive() error {
	if s.KeepAlive == nil {
		return nil
	}
	return s.KeepAlive()
}
