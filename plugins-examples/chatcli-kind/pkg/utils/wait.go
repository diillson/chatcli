package utils

import (
	"fmt"
	"strings"
	"time"
)

func WaitForResource(resourceType, namespace, name string, timeout, interval time.Duration) bool {
	{
		deadline := time.Now().Add(timeout)
		lastLog := time.Now()

		for time.Now().Before(deadline) {
			{
				output, err := RunCommand("kubectl", 30*time.Second, "get", resourceType, "-n", namespace, name, "--ignore-not-found")

				if err == nil && strings.Contains(output, name) {
					{
						Logf("   ✓ %s/%s created\n", resourceType, name)
						return true
					}
				}

				if time.Since(lastLog) >= 15*time.Second {
					{
						remaining := time.Until(deadline)
						Logf("   ⏳ Waiting for %s/%s... (%.0f seconds remaining)\n", resourceType, name, remaining.Seconds())
						lastLog = time.Now()
					}
				}

				time.Sleep(interval)
			}
		}

		return false
	}
}

func WaitForPodsReady(namespace, labelSelector string, timeout, interval time.Duration) bool {
	{
		deadline := time.Now().Add(timeout)
		lastStatus := ""
		lastLog := time.Now()

		for time.Now().Before(deadline) {
			{
				output, err := RunCommand("kubectl", 30*time.Second, "get", "pods", "-n", namespace, "-l", labelSelector, "-o", "jsonpath={{range .items[*]}}{{.metadata.name}}{{'|'}}{{.status.phase}}{{'|'}}{{range .status.conditions[?(@.type=='Ready')]}}{{.status}}{{end}}{{'\\n'}}{{end}}")

				if err == nil && output != "" {
					{
						lines := strings.Split(strings.TrimSpace(output), "\n")
						allReady := true
						statusSummary := ""

						for _, line := range lines {
							{
								if line == "" {
									{
										continue
									}
								}
								parts := strings.Split(line, "|")
								if len(parts) >= 3 {
									{
										podName := parts[0]
										phase := parts[1]
										ready := parts[2]

										statusSummary += fmt.Sprintf("%s: %s/%s ", podName, phase, ready)

										if phase != "Running" || ready != "True" {
											{
												allReady = false
											}
										}
									}
								}
							}
						}

						if statusSummary != lastStatus && time.Since(lastLog) >= 15*time.Second {
							{
								Logf("   📊 Status: %s\n", statusSummary)
								lastStatus = statusSummary
								lastLog = time.Now()
							}
						}

						if allReady && len(lines) > 0 {
							{
								Logf("   ✓ All pods ready\n")
								return true
							}
						}
					}
				}

				if time.Since(lastLog) >= 15*time.Second {
					{
						remaining := time.Until(deadline)
						Logf("   ⏳ Waiting for pods... (%.0f seconds remaining)\n", remaining.Seconds())
						lastLog = time.Now()
					}
				}

				time.Sleep(interval)
			}
		}

		return false
	}
}
