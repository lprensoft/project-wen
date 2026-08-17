package plugin

import "testing"

func TestNormalizeConfigEmptyStringByType(t *testing.T) {
	fields := []ConfigField{
		{Key: "n", Label: "数值", Type: FieldInt, Default: 100},
		{Key: "m", Label: "模式", Type: FieldSelect, Default: "fast",
			Options: []ConfigOption{{Value: "fast"}, {Value: "slow"}}},
		{Key: "s", Label: "短文本", Type: FieldString, Default: "缺省"},
		{Key: "t", Label: "长文本", Type: FieldText, Default: "缺省正文"},
	}

	// 界面对清空的 number input 提交的就是空串，那种情况必须理解为「用默认值」
	got, err := NormalizeConfig(fields, map[string]any{"n": "", "m": "", "s": "", "t": ""})
	if err != nil {
		t.Fatal(err)
	}
	if got["n"] != 100 {
		t.Errorf("int 空串应退回默认值，得到 %v", got["n"])
	}
	if got["m"] != "fast" {
		t.Errorf("select 空串应退回默认值，得到 %v", got["m"])
	}
	// 文本的空串是合法取值，否则用户清空一个文本框后保存会看到默认值又长回来
	if got["s"] != "" {
		t.Errorf("string 空串应保持为空，得到 %q", got["s"])
	}
	if got["t"] != "" {
		t.Errorf("text 空串应保持为空，得到 %q", got["t"])
	}

	// 键完全缺失时所有类型都取默认值
	got, err = NormalizeConfig(fields, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["s"] != "缺省" || got["t"] != "缺省正文" {
		t.Errorf("缺失的键应取默认值: %v", got)
	}
}

func TestNormalizeConfigTextNormalizesNewlines(t *testing.T) {
	fields := []ConfigField{{Key: "t", Type: FieldText, Default: ""}}
	got, err := NormalizeConfig(fields, map[string]any{"t": "一\r\n二\r三\n"})
	if err != nil {
		t.Fatal(err)
	}
	// 多行文本会被按行切分使用，残留的 \r 会跟到行尾让匹配莫名失败
	if got["t"] != "一\n二\n三\n" {
		t.Errorf("换行未统一: %q", got["t"])
	}
}

func TestNormalizeConfigTextRejectsNonString(t *testing.T) {
	fields := []ConfigField{{Key: "t", Label: "长文本", Type: FieldText, Default: ""}}
	if _, err := NormalizeConfig(fields, map[string]any{"t": 42}); err == nil {
		t.Error("非文本值应被拒绝")
	}
}

func TestNormalizeConfigTextDefaultNil(t *testing.T) {
	// 未写 Default 的文本字段（Default 为 nil）不应报错，应得到空串
	fields := []ConfigField{{Key: "t", Type: FieldText}}
	got, err := NormalizeConfig(fields, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["t"] != "" {
		t.Errorf("t = %v，want 空串", got["t"])
	}
}
