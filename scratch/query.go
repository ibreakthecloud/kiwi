package main

import (
	"fmt"
	"os"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type TaskState struct {
	ID          string    `json:"id" gorm:"primaryKey"`
	Task        string    `json:"task"`
	FilePath    string    `json:"file_path"`
	TestCmd     string    `json:"test_cmd"`
	Status      string    `json:"status"`
	Logs        string    `json:"logs"`
	Cost        float64   `json:"cost"`
}

func main() {
	db, err := gorm.Open(sqlite.Open("kiwi.db"), &gorm.Config{})
	if err != nil {
		fmt.Printf("Error opening DB: %v\n", err)
		os.Exit(1)
	}

	var tasks []TaskState
	err = db.Find(&tasks).Error
	if err != nil {
		fmt.Printf("Error querying tasks: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Found %d tasks in database:\n", len(tasks))
	for _, t := range tasks {
		fmt.Printf("- ID: %s, Task: %s, Status: %s, Cost: $%.2f\n", t.ID, t.Task, t.Status, t.Cost)
	}
}
