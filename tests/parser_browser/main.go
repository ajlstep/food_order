package main

import (
	"fmt"
	"log"
	"time"

	"github.com/mxschmitt/playwright-go"
)

func main() {
	if err := playwright.Install(); err != nil {
		log.Fatal(err)
	}

	pw, err := playwright.Run()
	if err != nil {
		log.Fatal(err)
	}
	defer pw.Stop()

	context, err := pw.Chromium.LaunchPersistentContext(
		"browser-data",
		playwright.BrowserTypeLaunchPersistentContextOptions{
			Headless: playwright.Bool(false),
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	defer context.Close()

	page, err := context.NewPage()
	if err != nil {
		log.Fatal(err)
	}

	_, err = page.Goto("https://999.md/ro/")
	if err != nil {
		log.Fatal(err)
	}

	// html, _ := page.Content()
	// fmt.Println(html)

	search := page.Locator(`[data-testid="header-search-input"]`)

	// Așteptăm să apară inputul
	err = search.WaitFor()
	if err != nil {
		log.Fatal(err)
	}

	// Scriem în el
	err = search.Fill("rtx 5090")
	if err != nil {
		log.Fatal(err)
	}

	// Facem căutarea
	err = search.Press("Enter")
	if err != nil {
		log.Fatal(err)
	}

	// Așteptăm să apară inputul
	err = search.WaitFor()
	if err != nil {
		log.Fatal(err)
	}

	cookieButton := page.GetByRole("button", playwright.PageGetByRoleOptions{
		Name: "Acceptă toate",
	})

	err = cookieButton.Click(
		playwright.LocatorClickOptions{
			Timeout: playwright.Float(5000),
		},
	)
	select {}

	time.Sleep(5 * time.Second)

	ads := page.Locator(`a:has(h4)`)

	count, err := ads.Count()
	fmt.Println(count)
	if err != nil {
		log.Fatal(err)
	}

	for i := 0; i < count; i++ {
		ad := ads.Nth(i)

		href, err := ad.GetAttribute("href")
		if err != nil {
			log.Println("href error:", err)
			continue
		}

		title, err := ad.Locator("h4").InnerText()
		if err != nil {
			log.Println("title error:", err)
			continue
		}

		fmt.Printf("Title: %s\n", title)
		fmt.Printf("Href: %s\n", href)
		fmt.Println("---")
	}

	select {}
}
