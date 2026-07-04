package main

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Task 是基准集中的一条评测用例。
type Task struct {
	ID           string `yaml:"id"`
	Task         string `yaml:"task"`
	ExpectRoute  string `yaml:"expect_route"`  // 留空 = 不计入路由准确率
	ExpectStatus string `yaml:"expect_status"` // OK / DENIED / FAILED
}

type suiteFile struct {
	Tasks []Task `yaml:"tasks"`
}

// LoadSuite 读取 YAML 基准集。
func LoadSuite(path string) ([]Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sf suiteFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return nil, err
	}
	return sf.Tasks, nil
}
