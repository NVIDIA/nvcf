// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tmaxmax/go-sse"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type EchoMessage struct {
	Message string  `json:"message" binding:"required"`
	Delay   float64 `json:"delay" default:"0.00001"`
	Repeats int     `json:"repeats" default:"1"`
	Size    int     `json:"size" default:"0"`
	Stream  bool    `json:"stream" default:"false"`
}

// src: https://stackoverflow.com/questions/22892120/how-to-generate-a-random-string-of-a-fixed-length-in-go (RandStringBytesMaskImprSrcSB)

const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
const (
	letterIdxBits = 6                    // 6 bits to represent a letter index
	letterIdxMask = 1<<letterIdxBits - 1 // All 1-bits, as many as letterIdxBits
	letterIdxMax  = 63 / letterIdxBits   // # of letter indices fitting in 63 bits
)

var src = rand.NewSource(time.Now().UnixNano())

func GenerateRandomString(n int) string {
	sb := strings.Builder{}
	sb.Grow(n)
	// A src.Int63() generates 63 random bits, enough for letterIdxMax characters!
	for i, cache, remain := n-1, src.Int63(), letterIdxMax; i >= 0; {
		if remain == 0 {
			cache, remain = src.Int63(), letterIdxMax
		}
		if idx := int(cache & letterIdxMask); idx < len(letterBytes) {
			sb.WriteByte(letterBytes[idx])
			i--
		}
		cache >>= letterIdxBits
		remain--
	}
	return sb.String()
}

func main() {
	r := gin.New()
	r.Use(
		gin.Recovery(),
	)

	r.GET("/health", func(c *gin.Context) {
		c.Writer.WriteHeader(http.StatusOK)
	})
	r.POST("/echo", func(c *gin.Context) {
		var echo EchoMessage
		if err := c.ShouldBindJSON(&echo); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse request"})
			return
		}

		if echo.Delay <= 0 {
			echo.Delay = 0.0000001
		}
		if echo.Size > 0 {
			echo.Message = GenerateRandomString(echo.Size)
		}
		if echo.Repeats == 0 {
			echo.Repeats = 1
			echo.Message = ""
		}

		if echo.Stream && c.Request.Header.Get("Accept") == "text/event-stream" {
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			m := &sse.Message{}
			m.AppendData(echo.Message)

			for i := 0; i < echo.Repeats; i++ {
				if echo.Delay <= 0.01 {
					_, _ = m.WriteTo(c.Writer)
					c.Writer.Flush()
					startTime := time.Now()
					for time.Now().Sub(startTime).Nanoseconds() < int64(echo.Delay*1e9) {
					}
				} else {
					time.Sleep(time.Duration(echo.Delay * float64(time.Second)))
					_, _ = m.WriteTo(c.Writer)
					c.Writer.Flush()
				}
			}
		} else {
			if echo.Delay <= 0.01 {
				startTime := time.Now()
				for time.Now().Sub(startTime).Nanoseconds() < int64(echo.Delay*1e9) {
				}
			} else {
				time.Sleep(time.Duration(echo.Delay * float64(time.Second)))
			}
			response := strings.Repeat(echo.Message, echo.Repeats)
			c.Header("Content-Length", strconv.Itoa(len(response)))
			c.String(http.StatusOK, response)
		}
	})

	r.POST("/ping-pong", func(c *gin.Context) {
		log.Printf("Ping-pong session started for %s", c.Request.URL.Path)
		err := http.NewResponseController(c.Writer).EnableFullDuplex()
		if err != nil {
			log.Printf("Error enabling full duplex for %s: %v", c.Request.URL.Path, err)
			c.AbortWithError(http.StatusInternalServerError, err)
			return
		}
		c.Writer.Header().Set("Content-Type", "application/octet-stream")
		c.Writer.WriteHeader(http.StatusOK)
		c.Writer.Flush()

		scanner := bufio.NewScanner(c.Request.Body)
		defer c.Request.Body.Close()

		for scanner.Scan() {
			line := scanner.Text()
			if line == "ping" {
				log.Printf("Received 'ping' from /ping-pong")
				_, writeErr := c.Writer.WriteString("pong\n")
				if writeErr != nil {
					log.Printf("Error writing 'pong' to client for %s: %v", c.Request.URL.Path, writeErr)
					c.AbortWithError(http.StatusInternalServerError, writeErr)
				}
				c.Writer.Flush()
				log.Printf("Sent 'pong' to /ping-pong")
			} else {
				err := fmt.Errorf("unexpected message: %s", line)
				log.Printf("error: %v", err)
				c.AbortWithError(http.StatusBadRequest, err)
			}
		}
		if err := scanner.Err(); err != nil {
			log.Printf("Error reading from client for %s (scanner): %v", c.Request.URL.Path, err)
			c.AbortWithError(http.StatusInternalServerError, err)
		}
		log.Printf("Ping-pong session ended for %s", c.Request.URL.Path)
	})

	h2s := &http2.Server{}
	server := &http.Server{
		Addr:    "0.0.0.0:8000",
		Handler: h2c.NewHandler(r, h2s),
	}
	log.Println("listening on :8000")
	log.SetOutput(os.Stderr)
	err := server.ListenAndServe()
	if err != nil {
		log.Panic(err)
	}
}
