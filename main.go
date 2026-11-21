package main // Declare the main package.

import ( // Import required packages.
	"context"       // Provide request cancellation and timeouts.
	"crypto/sha256" // Calculate SHA256 checksums.
	"encoding/hex"  // Encode checksums as hexadecimal strings.
	"fmt"           // Format errors and messages.
	"io"            // Read and write response data.
	"log/slog"      // Provide structured logging.
	"mime"          // Parse HTTP content disposition headers.
	"net/http"      // Perform HTTP requests.
	"net/url"       // Parse and resolve URLs.
	"os"            // Handle files and directories.
	"path/filepath" // Construct filesystem paths.
	"regexp"        // Extract asset URLs.
	"strings"       // Manipulate strings.
	"time"          // Handle delays and timeouts.

	"github.com/PuerkitoBio/goquery" // Parse HTML documents.
)

const ( // Define application configuration.
	outputDirectory = "PDFs"                                                    // Set the PDF output directory.
	requestDelay    = time.Second                                               // Set the delay between requests.
	maxPDFSize      = 100 * 1024 * 1024                                         // Set the maximum PDF size to 100 MB.
	assetIDStart    = 0                                                         // Set the first asset ID to try.
	assetIDEnd      = 1000                                                      // Set the last asset ID to try (inclusive).
	assetURLFormat  = "https://tormach.com/docs/download/assetlink/asset_id/%d" // Set the asset URL format.
)

var machinePages = []string{ // Define the machine pages to scrape.
	"https://tormach.com/af50-plus-edu-bundle.html",
	"https://tormach.com/xstech-pro-cnc-router-mill.html",
	"https://tormach.com/machines/routers/xstech-cnc-router-mill.html",
	"https://tormach.com/pcnc-440-base-edu-bundle.html",
	"https://tormach.com/pcnc-440-intermediate-edu-bundle.html",
	"https://tormach.com/pcnc-440-advanced-edu-bundle.html",
	"https://tormach.com/770m-base-edu-bundle.html",
	"https://tormach.com/770m-intermediate-edu-bundle.html",
	"https://tormach.com/770m-advanced-edu-bundle.html",
	"https://tormach.com/1100m-base-edu-bundle.html",
	"https://tormach.com/1100m-intermediate-edu-bundle.html",
	"https://tormach.com/1100m-advanced-edu-bundle.html",
	"https://tormach.com/770mx-ml-basic-edu-bundle.html",
	"https://tormach.com/770mx-ml-advanced-education-bundle.html",
	"https://tormach.com/1100mx-basic-edu-bundle.html",
	"https://tormach.com/1100mx-advanced-edu-bundle.html",
	"https://tormach.com/15lx-gang-tool-cnc-lathe-edu-package.html",
	"https://tormach.com/15lx-cnc-lathe-with-turret-edu-bundle.html",
	"https://tormach.com/8l-basic-edu-bundle.html",
	"https://tormach.com/8l-advanced-edu-bundle.html",
	"https://tormach.com/24r-basic-edu-bundle.html",
	"https://tormach.com/24r-intermediate-edu-bundle.html",
	"https://tormach.com/24r-advanced-edu-bundle.html",
	"https://tormach.com/machines/mills/1500mx.html",
	"https://tormach.com/machines/mills/1100mx.html",
	"https://tormach.com/machines/mills/770mx.html",
	"https://tormach.com/machines/mills/1100m.html",
	"https://tormach.com/machines/mills/770m.html",
	"https://tormach.com/machines/mills/pcnc-440.html",
	"https://tormach.com/machines/routers/24r-cnc-router.html",
	"https://tormach.com/machines/routers/xstech-pro-cnc-router.html",
	"https://tormach.com/machines/routers/xstech-router.html",
	"https://tormach.com/machines/lathes/15lx-lathe.html",
	"https://tormach.com/machines/lathes/15l-slant-pro-lathe.html",
	"https://tormach.com/machines/lathes/8l-lathe.html",
	"https://tormach.com/machines/bandsaws.html",
	"https://tormach.com/machines/automation-solutions.html",
	"https://tormach.com/machines/education-bundles.html",
}

var assetURLPattern = regexp.MustCompile(`(?:https?://(?:www\.)?tormach\.com)?/docs/download/assetlink/asset_id/[0-9]+`) // Match Tormach asset URLs.

var assetIDPattern = regexp.MustCompile(`[0-9]+$`) // Match the asset ID at the end of a URL.

var httpClient = &http.Client{ // Configure the HTTP client.
	Timeout: 60 * time.Second, // Set the request timeout.
}

var logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{ // Configure the logger.
	Level: slog.LevelDebug, // Enable debug-level logging.
}))

// Main application entry point.
func main() {
	ctx := context.Background() // Create the application context.

	logger.Debug("Starting Tormach PDF scraper") // Log application startup.

	if err := os.MkdirAll(outputDirectory, 0755); err != nil { // Create the output directory.
		logger.Error("Failed to create output directory", "error", err) // Log directory errors.
		return                                                          // Stop execution.
	}

	knownHashes, err := loadExistingPDFHashes(outputDirectory) // Load existing PDF checksums.
	if err != nil {                                            // Check for filesystem errors.
		logger.Error("Failed to scan existing PDFs", "error", err) // Log the error.
		return                                                     // Stop execution.
	}

	logger.Debug("Loaded existing PDF hashes", "count", len(knownHashes)) // Log existing PDF count.

	discoveredURLs := make(map[string]struct{}) // Store unique asset URLs.

	for _, pageURL := range machinePages { // Iterate through machine pages.
		logger.Debug("Scraping machine page", "url", pageURL) // Log the current page.

		assetURLs, err := scrapeMachinePage(ctx, pageURL) // Extract asset URLs.
		if err != nil {                                   // Check scraping errors.
			logger.Error("Failed to scrape page", "url", pageURL, "error", err) // Log the error.
			continue                                                            // Process the next page.
		}

		for _, assetURL := range assetURLs { // Iterate through discovered URLs.
			discoveredURLs[assetURL] = struct{}{} // Store unique URLs.
		}

		logger.Debug("Finished scraping page", "url", pageURL, "assets", len(assetURLs)) // Log scraping results.
	}

	for id := assetIDStart; id <= assetIDEnd; id++ { // Iterate through the asset ID range.
		discoveredURLs[fmt.Sprintf(assetURLFormat, id)] = struct{}{} // Add the URL (deduplicated by the map).
	}

	logger.Debug("Total unique asset URLs discovered", "count", len(discoveredURLs)) // Log discovered URL count.

	for assetURL := range discoveredURLs { // Iterate through discovered assets.
		logger.Debug("Processing asset", "url", assetURL) // Log the asset URL.

		if err := downloadPDF(ctx, assetURL, knownHashes); err != nil { // Download and validate the PDF.
			logger.Error("Failed to process asset", "url", assetURL, "error", err) // Log download errors.
			continue                                                               // Process the next asset.
		}
	}

	logger.Debug("Tormach PDF scraping completed") // Log application completion.
}

// Scrape a machine page and extract matching asset URLs.
func scrapeMachinePage(ctx context.Context, pageURL string) ([]string, error) {
	time.Sleep(requestDelay) // Wait before sending the request.

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil) // Create an HTTP request.
	if err != nil {                                                               // Check request creation errors.
		return nil, fmt.Errorf("create request: %w", err) // Return the error.
	}

	request.Header.Set("User-Agent", "TormachDocumentationResearchBot/1.0") // Identify the scraper.
	request.Header.Set("Accept", "text/html,application/xhtml+xml")         // Request HTML content.

	response, err := httpClient.Do(request) // Execute the HTTP request.
	if err != nil {                         // Check network errors.
		return nil, fmt.Errorf("request page: %w", err) // Return the error.
	}
	defer response.Body.Close() // Close the response body.

	if response.StatusCode != http.StatusOK { // Validate the HTTP status.
		return nil, fmt.Errorf("unexpected HTTP status: %s", response.Status) // Return the error.
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 20*1024*1024)) // Read up to 20 MB.
	if err != nil {                                                      // Check reading errors.
		return nil, fmt.Errorf("read page: %w", err) // Return the error.
	}

	document, err := goquery.NewDocumentFromReader(strings.NewReader(string(body))) // Parse the HTML document.
	if err != nil {                                                                 // Check HTML parsing errors.
		return nil, fmt.Errorf("parse HTML: %w", err) // Return the error.
	}

	baseURL, err := url.Parse(pageURL) // Parse the original page URL.
	if err != nil {                    // Check URL parsing errors.
		return nil, fmt.Errorf("parse base URL: %w", err) // Return the error.
	}

	foundURLs := make(map[string]struct{}) // Store discovered URLs.

	addMatchingURLs := func(content string) { // Define the URL extraction helper.
		matches := assetURLPattern.FindAllString(content, -1) // Find all matching URLs.

		for _, match := range matches { // Iterate through matches.
			parsedURL, parseErr := url.Parse(match) // Parse the matched URL.
			if parseErr != nil {                    // Check URL parsing errors.
				logger.Debug("Ignoring invalid asset URL", "url", match) // Log invalid URLs.
				continue                                                 // Skip the invalid URL.
			}

			resolvedURL := baseURL.ResolveReference(parsedURL) // Resolve relative URLs.
			resolvedURL.Fragment = ""                          // Remove URL fragments.

			if resolvedURL.Scheme != "https" || resolvedURL.Host != "tormach.com" { // Validate the destination.
				logger.Debug("Ignoring unexpected asset host", "url", resolvedURL.String()) // Log unexpected hosts.
				continue                                                                    // Skip unexpected destinations.
			}

			foundURLs[resolvedURL.String()] = struct{}{} // Store the normalized URL.
		}
	}

	addMatchingURLs(string(body)) // Search the complete HTML source.

	document.Find("a[href], iframe[src], embed[src], object[data]").Each(func(_ int, element *goquery.Selection) { // Inspect relevant HTML elements.
		for _, attribute := range []string{"href", "src", "data"} { // Iterate through relevant attributes.
			if value, exists := element.Attr(attribute); exists { // Read the attribute value.
				addMatchingURLs(value) // Extract matching URLs.
			}
		}
	})

	result := make([]string, 0, len(foundURLs)) // Initialize the results slice.

	for assetURL := range foundURLs { // Iterate through discovered URLs.
		result = append(result, assetURL) // Append the URL.
	}

	return result, nil // Return discovered URLs.
}

// Download a PDF and verify its SHA256 checksum.
func downloadPDF(ctx context.Context, assetURL string, knownHashes map[string]struct{}) error {
	time.Sleep(requestDelay) // Wait before downloading.

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil) // Create the download request.
	if err != nil {                                                                // Check request creation errors.
		return fmt.Errorf("create download request: %w", err) // Return the error.
	}

	request.Header.Set("User-Agent", "TormachDocumentationResearchBot/1.0") // Identify the scraper.
	request.Header.Set("Accept", "application/pdf,*/*")                     // Request PDF content.

	response, err := httpClient.Do(request) // Execute the download request.
	if err != nil {                         // Check network errors.
		return fmt.Errorf("download asset: %w", err) // Return the error.
	}
	defer response.Body.Close() // Close the response body.

	if response.StatusCode != http.StatusOK { // Validate the HTTP status.
		return fmt.Errorf("unexpected download status: %s", response.Status) // Return the error.
	}

	tempFile, err := os.CreateTemp(outputDirectory, ".download-*.tmp") // Create a temporary file.
	if err != nil {                                                    // Check temporary file creation errors.
		return fmt.Errorf("create temporary file: %w", err) // Return the error.
	}

	tempPath := tempFile.Name() // Store the temporary file path.
	defer os.Remove(tempPath)   // Remove the temporary file after processing.

	hasher := sha256.New() // Initialize the SHA256 hasher.

	size, err := io.Copy(io.MultiWriter(tempFile, hasher), io.LimitReader(response.Body, maxPDFSize+1)) // Download and calculate the checksum.
	if err != nil {                                                                                     // Check download errors.
		tempFile.Close()                        // Close the temporary file.
		return fmt.Errorf("write PDF: %w", err) // Return the error.
	}

	if size > maxPDFSize { // Check the maximum allowed size.
		tempFile.Close()                                                      // Close the temporary file.
		return fmt.Errorf("PDF exceeds maximum size of %d bytes", maxPDFSize) // Reject oversized files.
	}

	if err := tempFile.Sync(); err != nil { // Flush data to disk.
		tempFile.Close()                                  // Close the temporary file.
		return fmt.Errorf("sync temporary file: %w", err) // Return the error.
	}

	if _, err := tempFile.Seek(0, io.SeekStart); err != nil { // Rewind the temporary file.
		tempFile.Close()                                    // Close the temporary file.
		return fmt.Errorf("rewind temporary file: %w", err) // Return the error.
	}

	header := make([]byte, 5)              // Allocate the PDF signature buffer.
	_, err = io.ReadFull(tempFile, header) // Read the PDF signature.

	if err != nil || string(header) != "%PDF-" { // Validate the PDF signature.
		tempFile.Close()                                         // Close the temporary file.
		return fmt.Errorf("downloaded asset is not a valid PDF") // Reject invalid PDF content.
	}

	if err := tempFile.Close(); err != nil { // Close the temporary file.
		return fmt.Errorf("close temporary file: %w", err) // Return the error.
	}

	hash := hex.EncodeToString(hasher.Sum(nil)) // Convert SHA256 to hexadecimal.

	logger.Debug("Calculated PDF SHA256", "hash", hash, "size", size) // Log checksum information.

	if _, exists := knownHashes[hash]; exists { // Check whether the PDF already exists.
		logger.Debug("Duplicate PDF skipped", "url", assetURL, "sha256", hash) // Log duplicate detection.
		return nil                                                             // Skip saving duplicate content.
	}

	filename := getPDFFileName(response, assetURL)                // Determine the PDF filename.
	filename = uniquePDFFileName(outputDirectory, filename, hash) // Generate a unique filename.

	destination := filepath.Join(outputDirectory, filename) // Construct the destination path.

	if err := os.Rename(tempPath, destination); err != nil { // Move the temporary file.
		return fmt.Errorf("save PDF: %w", err) // Return the error.
	}

	knownHashes[hash] = struct{}{} // Register the new checksum.

	logger.Debug("PDF downloaded successfully", "filename", filename, "sha256", hash, "size", size) // Log download success.

	return nil // Finish processing.
}

// Load SHA256 hashes of existing PDFs.
func loadExistingPDFHashes(directory string) (map[string]struct{}, error) {
	hashes := make(map[string]struct{}) // Initialize the hash collection.

	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error { // Walk the output directory.
		if walkErr != nil { // Check filesystem errors.
			return walkErr // Return the error.
		}

		if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".pdf") { // Skip directories and non-PDF files.
			return nil // Continue scanning.
		}

		file, err := os.Open(path) // Open the existing PDF.
		if err != nil {            // Check file opening errors.
			return err // Return the error.
		}

		hasher := sha256.New()              // Initialize the checksum calculator.
		_, copyErr := io.Copy(hasher, file) // Calculate the existing PDF checksum.
		closeErr := file.Close()            // Close the existing PDF.

		if copyErr != nil { // Check hashing errors.
			return copyErr // Return the error.
		}

		if closeErr != nil { // Check closing errors.
			return closeErr // Return the error.
		}

		hash := hex.EncodeToString(hasher.Sum(nil)) // Convert the checksum to hexadecimal.
		hashes[hash] = struct{}{}                   // Store the checksum.

		logger.Debug("Existing PDF indexed", "filename", filepath.Base(path), "sha256", hash) // Log the indexed PDF.

		return nil // Continue scanning.
	})

	return hashes, err // Return the checksums and any error.
}

// Sanitize filenames to contain only lowercase letters, numbers, and underscores.
func sanitizeFileName(filename string) string {
	filename = strings.ToLower(filename)                            // Convert the filename to lowercase.
	filename = strings.TrimSuffix(filename, filepath.Ext(filename)) // Remove the extension.

	var builder strings.Builder // Create the filename builder.

	for _, character := range filename { // Iterate through filename characters.
		if character >= 'a' && character <= 'z' { // Check for lowercase letters.
			builder.WriteRune(character) // Append the letter.
		} else if character >= '0' && character <= '9' { // Check for digits.
			builder.WriteRune(character) // Append the digit.
		} else { // Handle invalid characters and underscores.
			if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "_") { // Prevent leading and consecutive underscores.
				builder.WriteRune('_') // Append a single underscore.
			}
		}
	}

	result := strings.Trim(builder.String(), "_") // Remove leading and trailing underscores.

	if result == "" { // Check for an empty filename.
		result = "document" // Use a safe fallback.
	}

	return result + ".pdf" // Return the sanitized PDF filename.
}

// Determine a PDF filename from response headers or the asset URL.
func getPDFFileName(response *http.Response, assetURL string) string {
	if disposition := response.Header.Get("Content-Disposition"); disposition != "" { // Check the response headers.
		_, parameters, err := mime.ParseMediaType(disposition) // Parse the content disposition.
		if err == nil {                                        // Check successful parsing.
			if filename := filepath.Base(parameters["filename"]); filename != "." && filename != "" { // Validate the filename.
				return sanitizeFileName(filename) // Sanitize the supplied filename.
			}
		}
	}

	parsedURL, err := url.Parse(assetURL) // Parse the asset URL.
	if err == nil {                       // Check URL parsing.
		matches := assetIDPattern.FindString(parsedURL.Path) // Extract the asset ID.
		if matches != "" {                                   // Check for an asset ID.
			return sanitizeFileName("asset_" + matches) // Generate a sanitized filename.
		}
	}

	return "document.pdf" // Return a safe fallback filename.
}

// Generate a unique filename when different PDFs share the same filename.
func uniquePDFFileName(directory string, filename string, hash string) string {
	filename = sanitizeFileName(filename) // Sanitize the filename.

	destination := filepath.Join(directory, filename) // Construct the destination path.

	if _, err := os.Stat(destination); os.IsNotExist(err) { // Check whether the filename is available.
		return filename // Return the original filename.
	}

	extension := filepath.Ext(filename)             // Extract the PDF extension.
	name := strings.TrimSuffix(filename, extension) // Remove the extension.

	return sanitizeFileName(name + "_" + hash[:12]) // Append a SHA256 suffix.
}
