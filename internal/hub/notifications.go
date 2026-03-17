package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"time"

	"github.com/digital-duck/momagrid/internal/schema"
)

type Notifier struct {
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	FromAddress  string
}

func (n *Notifier) Notify(job schema.JobStatusResponse, notify schema.JobNotify) {
	if notify.WebhookURL != "" {
		go n.sendWebhook(notify.WebhookURL, job)
	}
	if notify.Email != "" {
		go n.sendEmail(notify.Email, job)
	}
}

func (n *Notifier) sendWebhook(url string, job schema.JobStatusResponse) {
	body, _ := json.Marshal(job)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("webhook notification failed for job %s: %v", job.JobID, err)
		return
	}
	resp.Body.Close()
	log.Printf("webhook notification sent for job %s to %s", job.JobID, url)
}

func (n *Notifier) sendEmail(to string, job schema.JobStatusResponse) {
	if n.SMTPHost == "" {
		// Try env vars as fallback
		n.SMTPHost = os.Getenv("IGRID_SMTP_HOST")
		n.SMTPPort = 587
		n.SMTPUser = os.Getenv("IGRID_SMTP_USER")
		n.SMTPPassword = os.Getenv("IGRID_SMTP_PASSWORD")
		n.FromAddress = os.Getenv("IGRID_FROM_ADDRESS")
	}

	if n.SMTPHost == "" {
		log.Printf("email notification skipped for job %s: SMTP not configured", job.JobID)
		return
	}

	subject := fmt.Sprintf("Momagrid Job %s: %s", job.JobID, job.State)
	body := fmt.Sprintf("Your Momagrid job is %s.\n\nJob ID: %s\nModel: %s\nUpdated: %s\n\n", 
		job.State, job.JobID, job.Model, job.UpdatedAt.Format(time.RFC1123))

	if job.Result != nil {
		if job.Result.Error != "" {
			body += fmt.Sprintf("Error: %s\n", job.Result.Error)
		} else {
			body += "Result:\n" + job.Result.Content + "\n"
		}
	}

	msg := []byte("To: " + to + "\r\n" +
		"From: " + n.FromAddress + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"\r\n" +
		body + "\r\n")

	auth := smtp.PlainAuth("", n.SMTPUser, n.SMTPPassword, n.SMTPHost)
	err := smtp.SendMail(fmt.Sprintf("%s:%d", n.SMTPHost, n.SMTPPort), auth, n.FromAddress, []string{to}, msg)
	if err != nil {
		log.Printf("email notification failed for job %s to %s: %v", job.JobID, to, err)
		return
	}
	log.Printf("email notification sent for job %s to %s", job.JobID, to)
}
