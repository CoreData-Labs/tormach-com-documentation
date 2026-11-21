package main // Declare the main package.

import ( // Import the required packages.
	"bufio"         // Peek at the start of a response body without consuming it.
	"bytes"         // Search byte slices for the PDF signature.
	"context"       // Provide cancellation and timeouts across goroutines.
	"crypto/sha256" // Calculate SHA256 checksums of PDF content.
	"encoding/hex"  // Encode checksums as hexadecimal strings.
	"errors"        // Create and inspect errors.
	"fmt"           // Format errors and messages.
	"io"            // Read and write response data.
	"io/fs"         // Detect "file does not exist" errors and walk directories.
	"log/slog"      // Provide structured logging.
	"math/rand/v2"  // Pick random retry delays.
	"mime"          // Parse HTTP Content-Disposition headers.
	"net/http"      // Perform HTTP requests.
	"net/url"       // Parse and resolve URLs.
	"os"            // Handle files and directories.
	"os/signal"     // Handle Ctrl+C for a graceful shutdown.
	"path/filepath" // Construct filesystem paths.
	"regexp"        // Extract asset URLs and asset IDs.
	"sort"          // Sort asset URLs by numeric asset ID.
	"strconv"       // Convert asset IDs to integers.
	"strings"       // Manipulate strings.
	"sync"          // Coordinate goroutines and protect shared data.
	"sync/atomic"   // Maintain lock-free statistics counters.
	"syscall"       // Reference the termination signal.
	"time"          // Handle delays and timeouts.

	"github.com/PuerkitoBio/goquery" // Parse HTML documents.
)

const ( // Define the application configuration.
	pdfOutputDirectory      = "PDFs"                                                                                                           // Set the directory where PDFs are saved.
	temporaryFilePattern    = ".download-*.tmp"                                                                                                // Set the naming pattern of in-progress downloads.
	pdfFileExtension        = ".pdf"                                                                                                           // Set the extension given to saved files.
	pdfSignature            = "%PDF-"                                                                                                          // Set the bytes that every PDF contains near its start.
	pdfSignatureSearchBytes = 1024                                                                                                             // Set how many leading bytes may contain the PDF signature.
	firstAssetID            = 0                                                                                                                // Set the first asset ID to try.
	lastAssetID             = 1000                                                                                                             // Set the last asset ID to try (inclusive).
	assetURLFormat          = "https://tormach.com/docs/download/assetlink/asset_id/%d"                                                        // Set the asset URL format.
	tormachHost             = "tormach.com"                                                                                                    // Set the only host that may be downloaded from.
	tormachWWWHost          = "www.tormach.com"                                                                                                // Set the alternate host that is rewritten to the main host.
	scraperUserAgent        = "Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36" // Set the browser-style User-Agent sent with every request.
	concurrentWorkerCount   = 3                                                                                                                // Set how many workers run at the same time.
	minimumRequestInterval  = 500 * time.Millisecond                                                                                           // Set the minimum gap between any two request starts.
	httpRequestTimeout      = 3 * time.Minute                                                                                                  // Set the timeout of a single HTTP request.
	maximumRetryCount       = 3                                                                                                                // Set how many retries follow the first failed attempt.
	minimumRetryDelay       = 30 * time.Second                                                                                                 // Set the shortest wait before a retry.
	maximumRetryDelay       = 60 * time.Second                                                                                                 // Set the longest wait before a retry.
)

var machinePageURLs = []string{ // Define the machine pages to scrape.
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

var linkAttributeNames = []string{"href", "src", "data"} // Define the HTML attributes that may hold asset links.

var assetURLPattern = regexp.MustCompile(`(?:https?://(?:www\.)?tormach\.com)?/docs/download/assetlink/asset_id/[0-9]+`) // Match Tormach asset URLs.

var trailingNumberPattern = regexp.MustCompile(`[0-9]+$`) // Match the asset ID at the end of a URL path.

var errNotAPDF = errors.New("downloaded content is not a PDF") // Define the error for responses that are not PDFs.

var httpClient = &http.Client{ // Configure the shared HTTP client.
	Timeout: httpRequestTimeout, // Limit how long a single request may take.
	Transport: &http.Transport{ // Configure connection reuse for concurrent workers.
		Proxy:               http.ProxyFromEnvironment, // Honor proxy environment variables.
		MaxIdleConns:        concurrentWorkerCount * 2, // Keep a small pool of idle connections.
		MaxIdleConnsPerHost: concurrentWorkerCount,     // Keep one idle connection per worker for the host.
		MaxConnsPerHost:     concurrentWorkerCount,     // Hard-cap simultaneous connections to the host.
		IdleConnTimeout:     90 * time.Second,          // Close connections that stay idle too long.
		TLSHandshakeTimeout: 15 * time.Second,          // Fail slow TLS handshakes quickly.
	},
}

var rootLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{ // Configure the application logger.
	Level: slog.LevelDebug, // Emit debug-level messages and everything above.
}))

// permanentError marks a failure that retrying cannot fix.
type permanentError struct {
	cause error // Store the underlying error.
}

// Error returns the message of the underlying error.
func (e *permanentError) Error() string {
	return e.cause.Error() // Delegate to the underlying error.
}

// Unwrap exposes the underlying error to errors.Is and errors.As.
func (e *permanentError) Unwrap() error {
	return e.cause // Return the underlying error.
}

// httpStatusError describes a response with an unexpected HTTP status.
type httpStatusError struct {
	requestURL string // Store the URL that was requested.
	statusCode int    // Store the numeric HTTP status code.
	status     string // Store the full HTTP status text.
}

// Error formats the unexpected HTTP status for logs.
func (e *httpStatusError) Error() string {
	return fmt.Sprintf("unexpected HTTP status %s for %s", e.status, e.requestURL) // Describe the status and URL.
}

// downloadOutcome describes what happened to a successfully processed asset.
type downloadOutcome int

const ( // Define the possible download outcomes.
	outcomeUnknown   downloadOutcome = iota // Mark an outcome that was never determined.
	outcomeSaved                            // Mark a new PDF that was saved to disk.
	outcomeDuplicate                        // Mark a PDF that already existed on disk.
)

// runStatistics counts what happened during the run.
type runStatistics struct {
	pagesScrapedCount      atomic.Int64 // Count machine pages scraped successfully.
	pagesFailedCount       atomic.Int64 // Count machine pages skipped after failures.
	pdfsSavedCount         atomic.Int64 // Count new PDFs saved to disk.
	duplicatePDFsCount     atomic.Int64 // Count PDFs skipped because they already existed.
	nonPDFAssetsCount      atomic.Int64 // Count assets skipped because they were not PDFs.
	missingAssetsCount     atomic.Int64 // Count assets skipped because the server returned 404.
	failedAssetsCount      atomic.Int64 // Count assets skipped after other failures.
	interruptedAssetsCount atomic.Int64 // Count assets abandoned because of shutdown.
}

// logSummary writes the final statistics to the log.
func (s *runStatistics) logSummary(elapsed time.Duration) {
	rootLogger.Debug("Run summary", // Log the summary header.
		"elapsed", elapsed.Round(time.Second), // Include the total runtime.
		"pagesScraped", s.pagesScrapedCount.Load(), // Include scraped page count.
		"pagesFailed", s.pagesFailedCount.Load(), // Include failed page count.
		"pdfsSaved", s.pdfsSavedCount.Load(), // Include saved PDF count.
		"duplicatePDFs", s.duplicatePDFsCount.Load(), // Include duplicate PDF count.
		"nonPDFAssets", s.nonPDFAssetsCount.Load(), // Include non-PDF asset count.
		"missingAssets", s.missingAssetsCount.Load(), // Include missing asset count.
		"failedAssets", s.failedAssetsCount.Load(), // Include failed asset count.
		"interruptedAssets", s.interruptedAssetsCount.Load(), // Include interrupted asset count.
	)
}

// requestRateLimiter spaces out request starts across all workers.
type requestRateLimiter struct {
	ticker *time.Ticker // Store the ticker that releases one request slot per interval.
}

// newRequestRateLimiter creates a limiter that allows one request start per interval.
func newRequestRateLimiter(interval time.Duration) *requestRateLimiter {
	return &requestRateLimiter{ticker: time.NewTicker(interval)} // Start the ticker.
}

// waitForTurn blocks until the next request slot is available or the context ends.
func (l *requestRateLimiter) waitForTurn(ctx context.Context) error {
	select { // Wait for whichever event happens first.
	case <-ctx.Done(): // Handle cancellation.
		return ctx.Err() // Report why the wait ended.
	case <-l.ticker.C: // Handle the next free slot.
		return nil // Allow the request to proceed.
	}
}

// stop releases the resources held by the limiter.
func (l *requestRateLimiter) stop() {
	l.ticker.Stop() // Stop the ticker.
}

// assetURLCollection is a goroutine-safe set of asset URLs.
type assetURLCollection struct {
	mutex  sync.Mutex          // Protect the set from concurrent access.
	urlSet map[string]struct{} // Store each URL once.
}

// newAssetURLCollection creates an empty collection.
func newAssetURLCollection() *assetURLCollection {
	return &assetURLCollection{urlSet: make(map[string]struct{})} // Initialize the set.
}

// addAll adds URLs to the collection and returns how many were new.
func (c *assetURLCollection) addAll(urls []string) int {
	c.mutex.Lock()         // Lock the set.
	defer c.mutex.Unlock() // Unlock the set when finished.

	newURLCount := 0 // Count URLs that were not already present.

	for _, assetURL := range urls { // Iterate through the supplied URLs.
		if _, alreadyPresent := c.urlSet[assetURL]; !alreadyPresent { // Check whether the URL is new.
			newURLCount++ // Count the new URL.
		}

		c.urlSet[assetURL] = struct{}{} // Store the URL.
	}

	return newURLCount // Return the number of new URLs.
}

// size returns how many unique URLs are stored.
func (c *assetURLCollection) size() int {
	c.mutex.Lock()         // Lock the set.
	defer c.mutex.Unlock() // Unlock the set when finished.

	return len(c.urlSet) // Return the number of stored URLs.
}

// sortedByAssetID returns all URLs ordered by numeric asset ID.
func (c *assetURLCollection) sortedByAssetID() []string {
	c.mutex.Lock()         // Lock the set.
	defer c.mutex.Unlock() // Unlock the set when finished.

	sortedURLs := make([]string, 0, len(c.urlSet)) // Allocate the result slice.

	for assetURL := range c.urlSet { // Iterate through the stored URLs.
		sortedURLs = append(sortedURLs, assetURL) // Append the URL.
	}

	sort.Slice(sortedURLs, func(first int, second int) bool { // Sort by numeric asset ID.
		return extractAssetID(sortedURLs[first]) < extractAssetID(sortedURLs[second]) // Compare the numeric IDs.
	})

	return sortedURLs // Return the ordered URLs.
}

// pdfStore saves PDFs and remembers the checksums of everything saved so far.
type pdfStore struct {
	mutex       sync.Mutex          // Make duplicate checks, naming, and saving atomic.
	directory   string              // Store the output directory path.
	knownHashes map[string]struct{} // Store the SHA256 checksums of saved PDFs.
}

// newPDFStore creates an empty store for the given directory.
func newPDFStore(directory string) *pdfStore {
	return &pdfStore{directory: directory, knownHashes: make(map[string]struct{})} // Initialize the store.
}

// knownHashCount returns how many checksums are registered.
func (s *pdfStore) knownHashCount() int {
	s.mutex.Lock()         // Lock the store.
	defer s.mutex.Unlock() // Unlock the store when finished.

	return len(s.knownHashes) // Return the checksum count.
}

// indexExistingPDFs registers the checksums of PDFs already in the directory.
func (s *pdfStore) indexExistingPDFs(logger *slog.Logger) error {
	return filepath.WalkDir(s.directory, func(path string, entry fs.DirEntry, walkErr error) error { // Walk the output directory.
		if walkErr != nil { // Check filesystem errors.
			return walkErr // Return the error.
		}

		if entry.IsDir() { // Skip directories.
			return nil // Continue scanning.
		}

		if !strings.EqualFold(filepath.Ext(path), pdfFileExtension) { // Skip non-PDF files.
			logger.Debug("Skipping non-PDF file while indexing", "path", path) // Log the skipped file.
			return nil                                                         // Continue scanning.
		}

		fileHash, hashErr := calculateFileSHA256(path) // Calculate the checksum of the existing PDF.
		if hashErr != nil {                            // Check hashing errors.
			return hashErr // Return the error.
		}

		s.mutex.Lock()                       // Lock the store.
		s.knownHashes[fileHash] = struct{}{} // Register the checksum.
		s.mutex.Unlock()                     // Unlock the store.

		logger.Debug("Existing PDF indexed", "filename", filepath.Base(path), "sha256", fileHash) // Log the indexed PDF.

		return nil // Continue scanning.
	})
}

// saveIfNew moves a finished download into the directory unless its content already exists.
func (s *pdfStore) saveIfNew(logger *slog.Logger, temporaryPath string, preferredFileName string, fileHash string) (string, bool, error) {
	s.mutex.Lock()         // Lock the store so workers cannot race on names or checksums.
	defer s.mutex.Unlock() // Unlock the store when finished.

	if _, alreadyKnown := s.knownHashes[fileHash]; alreadyKnown { // Check whether the content already exists.
		logger.Debug("Checksum already known", "sha256", fileHash) // Log the duplicate.
		return "", false, nil                                      // Report that nothing was saved.
	}

	availableFileName, nameErr := s.chooseAvailableFileName(preferredFileName, fileHash) // Pick a filename that does not collide.
	if nameErr != nil {                                                                  // Check naming errors.
		return "", false, nameErr // Return the error.
	}

	destinationPath := filepath.Join(s.directory, availableFileName) // Construct the destination path.

	logger.Debug("Moving download into place", "from", temporaryPath, "to", destinationPath) // Log the move.

	if renameErr := os.Rename(temporaryPath, destinationPath); renameErr != nil { // Move the temporary file.
		return "", false, fmt.Errorf("save PDF: %w", renameErr) // Return the error.
	}

	s.knownHashes[fileHash] = struct{}{} // Register the new checksum.

	return availableFileName, true, nil // Report the saved filename.
}

// chooseAvailableFileName returns the preferred name, or a checksum-suffixed name if it is taken.
func (s *pdfStore) chooseAvailableFileName(preferredFileName string, fileHash string) (string, error) {
	_, statErr := os.Stat(filepath.Join(s.directory, preferredFileName)) // Check whether the preferred name exists.

	if errors.Is(statErr, fs.ErrNotExist) { // Check whether the name is free.
		return preferredFileName, nil // Use the preferred name.
	}

	if statErr != nil { // Check unexpected filesystem errors.
		return "", fmt.Errorf("check filename availability: %w", statErr) // Return the error.
	}

	nameWithoutExtension := strings.TrimSuffix(preferredFileName, pdfFileExtension) // Remove the extension.

	return nameWithoutExtension + "_" + fileHash[:12] + pdfFileExtension, nil // Append part of the checksum.
}

// processConcurrently runs processItem for every item using a pool of workers.
func processConcurrently[Item any](ctx context.Context, poolName string, workerCount int, items []Item, processItem func(ctx context.Context, workerLogger *slog.Logger, item Item)) {
	poolLogger := rootLogger.With("pool", poolName) // Tag every log line with the pool name.

	poolLogger.Debug("Starting worker pool", "workers", workerCount, "items", len(items)) // Log the pool start.

	itemChannel := make(chan Item) // Create the channel that hands items to workers.

	var workerGroup sync.WaitGroup // Track running workers.

	for workerNumber := 1; workerNumber <= workerCount; workerNumber++ { // Start each worker.
		workerGroup.Add(1) // Register the worker.

		go func(workerNumber int) { // Run the worker in its own goroutine.
			defer workerGroup.Done() // Mark the worker as finished on exit.

			workerLogger := poolLogger.With("worker", workerNumber) // Tag log lines with the worker number.

			workerLogger.Debug("Worker started") // Log the worker start.

			for item := range itemChannel { // Take items until the channel closes.
				if ctx.Err() != nil { // Check whether shutdown was requested.
					workerLogger.Debug("Skipping item because shutdown was requested") // Log the skip.
					continue                                                           // Drain remaining items quickly.
				}

				processItem(ctx, workerLogger, item) // Process the item.
			}

			workerLogger.Debug("Worker finished") // Log the worker end.
		}(workerNumber)
	}

feedItems: // Label the loop so the select can break out of it.
	for _, item := range items { // Feed each item to the workers.
		select { // Wait for a free worker or cancellation.
		case itemChannel <- item: // Hand the item to a worker.
		case <-ctx.Done(): // Stop feeding on shutdown.
			poolLogger.Debug("Stopped feeding items because shutdown was requested") // Log the early stop.
			break feedItems                                                          // Leave the feeding loop.
		}
	}

	close(itemChannel) // Tell workers that no more items are coming.

	workerGroup.Wait() // Wait for all workers to finish.

	poolLogger.Debug("Worker pool finished") // Log the pool end.
}

// sleepWithContext waits for the duration or until the context ends.
func sleepWithContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration) // Create the timer.
	defer timer.Stop()               // Release the timer on exit.

	select { // Wait for whichever event happens first.
	case <-ctx.Done(): // Handle cancellation.
		return ctx.Err() // Report why the wait ended.
	case <-timer.C: // Handle the timer firing.
		return nil // Report a completed wait.
	}
}

// chooseRandomRetryDelay returns a random delay between the minimum and maximum retry delay.
func chooseRandomRetryDelay() time.Duration {
	return minimumRetryDelay + rand.N(maximumRetryDelay-minimumRetryDelay) // Add random jitter to the minimum delay.
}

// runWithRetries runs the operation once and retries it on retryable failures.
func runWithRetries(ctx context.Context, operationLogger *slog.Logger, operationName string, operation func() error) error {
	var lastError error // Remember the most recent failure.

	for attemptNumber := 0; attemptNumber <= maximumRetryCount; attemptNumber++ { // Run the first attempt plus every retry.
		if attemptNumber > 0 { // Check whether this attempt is a retry.
			retryDelay := chooseRandomRetryDelay() // Pick how long to wait.

			operationLogger.Debug("Waiting before retry", "operation", operationName, "retry", attemptNumber, "maxRetries", maximumRetryCount, "delay", retryDelay.Round(time.Second)) // Log the wait.

			if sleepErr := sleepWithContext(ctx, retryDelay); sleepErr != nil { // Wait before retrying.
				operationLogger.Debug("Retry wait interrupted", "operation", operationName, "error", sleepErr) // Log the interruption.
				return sleepErr                                                                                // Stop retrying.
			}
		}

		operationLogger.Debug("Starting attempt", "operation", operationName, "attempt", attemptNumber+1, "maxAttempts", maximumRetryCount+1) // Log the attempt.

		lastError = operation() // Run the operation.

		if lastError == nil { // Check for success.
			operationLogger.Debug("Attempt succeeded", "operation", operationName, "attempt", attemptNumber+1) // Log the success.
			return nil                                                                                         // Stop retrying.
		}

		if ctx.Err() != nil { // Check whether shutdown caused the failure.
			operationLogger.Debug("Attempt abandoned because shutdown was requested", "operation", operationName, "error", lastError) // Log the abandonment.
			return ctx.Err()                                                                                                          // Stop retrying.
		}

		var permanentFailure *permanentError // Prepare to detect permanent failures.

		if errors.As(lastError, &permanentFailure) { // Check whether retrying is pointless.
			operationLogger.Debug("Attempt failed permanently, not retrying", "operation", operationName, "attempt", attemptNumber+1, "error", lastError) // Log the permanent failure.
			return lastError                                                                                                                              // Stop retrying.
		}

		operationLogger.Debug("Attempt failed, may retry", "operation", operationName, "attempt", attemptNumber+1, "error", lastError) // Log the retryable failure.
	}

	operationLogger.Debug("Giving up after all retries", "operation", operationName, "retries", maximumRetryCount, "error", lastError) // Log the give-up.

	return fmt.Errorf("giving up after %d retries: %w", maximumRetryCount, lastError) // Return the final error.
}

// isRetryableStatusCode reports whether a failed HTTP status may succeed later.
func isRetryableStatusCode(statusCode int) bool {
	return statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooEarly || statusCode == http.StatusTooManyRequests || statusCode >= 500 // Treat timeouts, rate limits, and server errors as temporary.
}

// newHTTPStatusError builds an error for an unexpected status, marked permanent when retrying is pointless.
func newHTTPStatusError(requestURL string, response *http.Response) error {
	statusError := &httpStatusError{requestURL: requestURL, statusCode: response.StatusCode, status: response.Status} // Describe the status.

	if isRetryableStatusCode(response.StatusCode) { // Check whether the status is temporary.
		return statusError // Allow retries.
	}

	return &permanentError{cause: statusError} // Forbid retries.
}

// newGetRequest builds a GET request carrying the scraper headers.
func newGetRequest(ctx context.Context, targetURL string, acceptHeader string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil) // Create the request.
	if err != nil {                                                                 // Check request creation errors.
		return nil, &permanentError{cause: fmt.Errorf("create request: %w", err)} // A malformed request can never succeed.
	}

	request.Header.Set("User-Agent", scraperUserAgent) // Send the browser-style User-Agent.
	request.Header.Set("Accept", acceptHeader)         // Declare the accepted content types.

	return request, nil // Return the request.
}

// extractAssetID returns the numeric ID at the end of an asset URL, or -1 if there is none.
func extractAssetID(assetURL string) int {
	parsedURL, parseErr := url.Parse(assetURL) // Parse the asset URL.
	if parseErr != nil {                       // Check URL parsing errors.
		return -1 // Signal that no ID was found.
	}

	idText := trailingNumberPattern.FindString(parsedURL.Path) // Find the trailing number.
	if idText == "" {                                          // Check whether a number exists.
		return -1 // Signal that no ID was found.
	}

	assetID, convertErr := strconv.Atoi(idText) // Convert the number.
	if convertErr != nil {                      // Check conversion errors.
		return -1 // Signal that no ID was found.
	}

	return assetID // Return the numeric ID.
}

// sanitizePDFFileName reduces a filename to lowercase letters, digits, and single underscores plus ".pdf", or returns "" if nothing usable remains.
func sanitizePDFFileName(rawFileName string) string {
	lowercaseName := strings.ToLower(rawFileName)                               // Convert the filename to lowercase.
	nameWithoutExtension := strings.TrimSuffix(lowercaseName, pdfFileExtension) // Remove only a trailing ".pdf".

	var sanitizedBuilder strings.Builder // Create the filename builder.

	lastCharacterWasUnderscore := true // Start true so leading separators are skipped.

	for _, character := range nameWithoutExtension { // Iterate through filename characters.
		isLowercaseLetter := character >= 'a' && character <= 'z' // Check for lowercase letters.
		isDigit := character >= '0' && character <= '9'           // Check for digits.

		if isLowercaseLetter || isDigit { // Keep letters and digits.
			sanitizedBuilder.WriteRune(character) // Append the character.
			lastCharacterWasUnderscore = false    // Record that the last character was not an underscore.
		} else if !lastCharacterWasUnderscore { // Replace a run of other characters with one underscore.
			sanitizedBuilder.WriteRune('_')   // Append a single underscore.
			lastCharacterWasUnderscore = true // Record that the last character was an underscore.
		}
	}

	sanitizedName := strings.Trim(sanitizedBuilder.String(), "_") // Remove a trailing underscore.

	if sanitizedName == "" { // Check for an empty filename.
		return "" // Signal that nothing usable remained.
	}

	return sanitizedName + pdfFileExtension // Return the sanitized PDF filename.
}

// determinePDFFileName picks a filename from response headers, falling back to "<asset ID>.pdf".
func determinePDFFileName(logger *slog.Logger, response *http.Response, assetURL string) (string, error) {
	if contentDisposition := response.Header.Get("Content-Disposition"); contentDisposition != "" { // Check the response headers.
		_, dispositionParameters, parseErr := mime.ParseMediaType(contentDisposition) // Parse the Content-Disposition header.

		if parseErr != nil { // Check parsing errors.
			logger.Debug("Could not parse Content-Disposition", "header", contentDisposition, "error", parseErr) // Log the parsing problem.
		} else if headerFileName := dispositionParameters["filename"]; headerFileName != "" { // Check for a supplied filename.
			if sanitizedHeaderName := sanitizePDFFileName(headerFileName); sanitizedHeaderName != "" { // Sanitize the supplied filename.
				logger.Debug("Using filename from Content-Disposition", "filename", sanitizedHeaderName) // Log the chosen source.
				return sanitizedHeaderName, nil                                                          // Use the supplied filename.
			}

			logger.Debug("Content-Disposition filename was unusable after sanitizing", "filename", headerFileName) // Log the problem.
		}
	}

	assetID := extractAssetID(assetURL) // Extract the asset ID from the URL.
	if assetID < 0 {                    // Check whether an ID exists.
		return "", fmt.Errorf("no usable filename and no asset ID in %s", assetURL) // Report that the asset cannot be named.
	}

	logger.Debug("Using asset ID as filename", "assetID", assetID) // Log the chosen source.

	return strconv.Itoa(assetID) + pdfFileExtension, nil // Return "<ID>.pdf".
}

// calculateFileSHA256 returns the hexadecimal SHA256 checksum of a file.
func calculateFileSHA256(path string) (string, error) {
	file, openErr := os.Open(path) // Open the file.
	if openErr != nil {            // Check file opening errors.
		return "", openErr // Return the error.
	}
	defer file.Close() // Close the file on exit.

	hasher := sha256.New() // Initialize the checksum calculator.

	if _, copyErr := io.Copy(hasher, file); copyErr != nil { // Feed the file through the hasher.
		return "", copyErr // Return the error.
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil // Return the checksum as hexadecimal.
}

// removeStaleTemporaryFiles deletes in-progress downloads left behind by an earlier crashed run.
func removeStaleTemporaryFiles(logger *slog.Logger, directory string) {
	staleFilePaths, globErr := filepath.Glob(filepath.Join(directory, temporaryFilePattern)) // Find leftover temporary files.
	if globErr != nil {                                                                      // Check pattern errors.
		logger.Error("Failed to search for stale temporary files", "error", globErr) // Log the error.
		return                                                                       // Skip the cleanup.
	}

	logger.Debug("Searched for stale temporary files", "count", len(staleFilePaths)) // Log how many were found.

	for _, staleFilePath := range staleFilePaths { // Iterate through leftover files.
		if removeErr := os.Remove(staleFilePath); removeErr != nil { // Delete the file.
			logger.Error("Failed to remove stale temporary file", "path", staleFilePath, "error", removeErr) // Log the error.
			continue                                                                                         // Process the next file.
		}

		logger.Debug("Removed stale temporary file", "path", staleFilePath) // Log the removal.
	}
}

// normalizeAssetURL resolves a matched URL against the page and validates its destination.
func normalizeAssetURL(logger *slog.Logger, pageBaseURL *url.URL, matchedURL string) (string, bool) {
	parsedURL, parseErr := url.Parse(matchedURL) // Parse the matched URL.
	if parseErr != nil {                         // Check URL parsing errors.
		logger.Debug("Ignoring invalid asset URL", "url", matchedURL, "error", parseErr) // Log invalid URLs.
		return "", false                                                                 // Skip the invalid URL.
	}

	resolvedURL := pageBaseURL.ResolveReference(parsedURL) // Resolve relative URLs against the page.
	resolvedURL.Fragment = ""                              // Remove URL fragments.

	if resolvedURL.Host == tormachWWWHost { // Check for the alternate host.
		resolvedURL.Host = tormachHost // Rewrite it to the main host.
	}

	if resolvedURL.Scheme == "http" { // Check for an insecure scheme.
		resolvedURL.Scheme = "https" // Upgrade it to HTTPS.
	}

	if resolvedURL.Scheme != "https" || resolvedURL.Host != tormachHost { // Validate the destination.
		logger.Debug("Ignoring unexpected asset host", "url", resolvedURL.String()) // Log unexpected hosts.
		return "", false                                                            // Skip unexpected destinations.
	}

	return resolvedURL.String(), true // Return the normalized URL.
}

// extractAssetURLsFromHTML finds every Tormach asset URL in a page.
func extractAssetURLsFromHTML(logger *slog.Logger, pageURL string, htmlBody []byte) ([]string, error) {
	pageBaseURL, parseErr := url.Parse(pageURL) // Parse the page URL.
	if parseErr != nil {                        // Check URL parsing errors.
		return nil, fmt.Errorf("parse page URL: %w", parseErr) // Return the error.
	}

	htmlDocument, documentErr := goquery.NewDocumentFromReader(bytes.NewReader(htmlBody)) // Parse the HTML document.
	if documentErr != nil {                                                               // Check HTML parsing errors.
		return nil, fmt.Errorf("parse HTML: %w", documentErr) // Return the error.
	}

	uniqueAssetURLs := make(map[string]struct{}) // Store discovered URLs without duplicates.

	addAssetURLsFromText := func(text string) { // Define the URL extraction helper.
		for _, matchedURL := range assetURLPattern.FindAllString(text, -1) { // Iterate through every match.
			normalizedURL, isValid := normalizeAssetURL(logger, pageBaseURL, matchedURL) // Normalize and validate the match.
			if !isValid {                                                                // Check validity.
				continue // Skip invalid matches.
			}

			if _, alreadyFound := uniqueAssetURLs[normalizedURL]; !alreadyFound { // Check whether the URL is new for this page.
				logger.Debug("Found asset URL on page", "assetURL", normalizedURL) // Log the discovery.
			}

			uniqueAssetURLs[normalizedURL] = struct{}{} // Store the normalized URL.
		}
	}

	addAssetURLsFromText(string(htmlBody)) // Search the complete HTML source.

	htmlDocument.Find("a[href], iframe[src], embed[src], object[data]").Each(func(_ int, element *goquery.Selection) { // Inspect elements that commonly link to files.
		for _, attributeName := range linkAttributeNames { // Iterate through relevant attributes.
			if attributeValue, exists := element.Attr(attributeName); exists { // Read the attribute value.
				addAssetURLsFromText(attributeValue) // Extract matching URLs from the decoded attribute.
			}
		}
	})

	extractedURLs := make([]string, 0, len(uniqueAssetURLs)) // Initialize the results slice.

	for assetURL := range uniqueAssetURLs { // Iterate through discovered URLs.
		extractedURLs = append(extractedURLs, assetURL) // Append the URL.
	}

	return extractedURLs, nil // Return the discovered URLs.
}

// fetchPageHTML downloads the HTML of a page once, without retrying.
func fetchPageHTML(ctx context.Context, pageLogger *slog.Logger, limiter *requestRateLimiter, pageURL string) ([]byte, error) {
	if waitErr := limiter.waitForTurn(ctx); waitErr != nil { // Respect the global request rate.
		return nil, waitErr // Return the cancellation error.
	}

	request, requestErr := newGetRequest(ctx, pageURL, "text/html,application/xhtml+xml") // Build the page request.
	if requestErr != nil {                                                                // Check request creation errors.
		return nil, requestErr // Return the error.
	}

	pageLogger.Debug("Sending page request") // Log the request.

	response, responseErr := httpClient.Do(request) // Execute the HTTP request.
	if responseErr != nil {                         // Check network errors.
		return nil, fmt.Errorf("request page: %w", responseErr) // Return a retryable error.
	}
	defer response.Body.Close() // Close the response body on exit.

	pageLogger.Debug("Received page response", "status", response.Status, "contentType", response.Header.Get("Content-Type"), "contentLength", response.ContentLength) // Log the response.

	if response.StatusCode != http.StatusOK { // Validate the HTTP status.
		return nil, newHTTPStatusError(pageURL, response) // Return a status error.
	}

	htmlBody, readErr := io.ReadAll(response.Body) // Read the whole page.
	if readErr != nil {                            // Check reading errors.
		return nil, fmt.Errorf("read page: %w", readErr) // Return a retryable error.
	}

	pageLogger.Debug("Read page body", "bytes", len(htmlBody)) // Log the body size.

	return htmlBody, nil // Return the HTML.
}

// scrapeMachinePage fetches a machine page with retries and records its asset URLs.
func scrapeMachinePage(ctx context.Context, workerLogger *slog.Logger, limiter *requestRateLimiter, statistics *runStatistics, collection *assetURLCollection, pageURL string) {
	pageLogger := workerLogger.With("pageURL", pageURL) // Tag log lines with the page URL.

	pageLogger.Debug("Scraping machine page") // Log the start.

	var discoveredAssetURLs []string // Hold the URLs found by the successful attempt.

	scrapeErr := runWithRetries(ctx, pageLogger, "scrape machine page", func() error { // Run the scrape with retries.
		htmlBody, fetchErr := fetchPageHTML(ctx, pageLogger, limiter, pageURL) // Download the page.
		if fetchErr != nil {                                                   // Check download errors.
			return fetchErr // Let the retry logic decide.
		}

		extractedURLs, extractErr := extractAssetURLsFromHTML(pageLogger, pageURL, htmlBody) // Extract asset URLs.
		if extractErr != nil {                                                               // Check extraction errors.
			return &permanentError{cause: extractErr} // Broken HTML will not fix itself.
		}

		discoveredAssetURLs = extractedURLs // Keep the result of this attempt.

		return nil // Report success.
	})

	if scrapeErr != nil { // Check whether the page was skipped.
		if ctx.Err() == nil { // Only count failures that were not caused by shutdown.
			statistics.pagesFailedCount.Add(1)                                           // Count the failed page.
			pageLogger.Error("Skipping machine page after failures", "error", scrapeErr) // Log the skip.
		}

		return // Move on to the next page.
	}

	newURLCount := collection.addAll(discoveredAssetURLs) // Store the discovered URLs.

	statistics.pagesScrapedCount.Add(1) // Count the scraped page.

	pageLogger.Debug("Finished scraping page", "assetsFound", len(discoveredAssetURLs), "newAssets", newURLCount) // Log the result.
}

// downloadAssetOnce downloads one asset and saves it if it is a new PDF, without retrying.
func downloadAssetOnce(ctx context.Context, assetLogger *slog.Logger, limiter *requestRateLimiter, store *pdfStore, assetURL string) (downloadOutcome, error) {
	if waitErr := limiter.waitForTurn(ctx); waitErr != nil { // Respect the global request rate.
		return outcomeUnknown, waitErr // Return the cancellation error.
	}

	request, requestErr := newGetRequest(ctx, assetURL, "application/pdf,*/*") // Build the download request.
	if requestErr != nil {                                                     // Check request creation errors.
		return outcomeUnknown, requestErr // Return the error.
	}

	assetLogger.Debug("Sending download request") // Log the request.

	response, responseErr := httpClient.Do(request) // Execute the download request.
	if responseErr != nil {                         // Check network errors.
		return outcomeUnknown, fmt.Errorf("download asset: %w", responseErr) // Return a retryable error.
	}
	defer response.Body.Close() // Close the response body on exit.

	assetLogger.Debug("Received download response", "status", response.Status, "contentType", response.Header.Get("Content-Type"), "contentLength", response.ContentLength, "contentDisposition", response.Header.Get("Content-Disposition")) // Log the response.

	if response.StatusCode != http.StatusOK { // Validate the HTTP status.
		return outcomeUnknown, newHTTPStatusError(assetURL, response) // Return a status error.
	}

	bufferedBody := bufio.NewReader(response.Body) // Wrap the body so its start can be inspected.

	signatureWindow, peekErr := bufferedBody.Peek(pdfSignatureSearchBytes) // Look at the first bytes without consuming them.
	if peekErr != nil && !errors.Is(peekErr, io.EOF) {                     // Ignore EOF because short files are valid input here.
		return outcomeUnknown, fmt.Errorf("read response start: %w", peekErr) // Return a retryable error.
	}

	if !bytes.Contains(signatureWindow, []byte(pdfSignature)) { // Validate the PDF signature before writing anything.
		assetLogger.Debug("Response does not contain a PDF signature", "inspectedBytes", len(signatureWindow)) // Log the rejection.
		return outcomeUnknown, &permanentError{cause: errNotAPDF}                                              // Reject non-PDF content permanently.
	}

	temporaryFile, createErr := os.CreateTemp(store.directory, temporaryFilePattern) // Create a temporary file.
	if createErr != nil {                                                            // Check temporary file creation errors.
		return outcomeUnknown, &permanentError{cause: fmt.Errorf("create temporary file: %w", createErr)} // Local disk problems will not fix themselves quickly.
	}

	temporaryPath := temporaryFile.Name() // Store the temporary file path.

	assetLogger.Debug("Created temporary file", "path", temporaryPath) // Log the temporary file.

	defer func() { // Clean up the temporary file when this function ends.
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) { // Ignore the normal case where it was already moved.
			assetLogger.Error("Failed to remove temporary file", "path", temporaryPath, "error", removeErr) // Log the cleanup failure.
		}
	}()

	hasher := sha256.New() // Initialize the SHA256 hasher.

	downloadedBytes, copyErr := io.Copy(io.MultiWriter(temporaryFile, hasher), bufferedBody) // Download while calculating the checksum.
	if copyErr != nil {                                                                      // Check download errors.
		_ = temporaryFile.Close()                                   // Close the temporary file.
		return outcomeUnknown, fmt.Errorf("write PDF: %w", copyErr) // Return a retryable error.
	}

	assetLogger.Debug("Finished downloading body", "bytes", downloadedBytes) // Log the download size.

	if syncErr := temporaryFile.Sync(); syncErr != nil { // Flush data to disk.
		_ = temporaryFile.Close()                                                                     // Close the temporary file.
		return outcomeUnknown, &permanentError{cause: fmt.Errorf("sync temporary file: %w", syncErr)} // Return the error.
	}

	if closeErr := temporaryFile.Close(); closeErr != nil { // Close the temporary file.
		return outcomeUnknown, &permanentError{cause: fmt.Errorf("close temporary file: %w", closeErr)} // Return the error.
	}

	fileHash := hex.EncodeToString(hasher.Sum(nil)) // Convert the SHA256 checksum to hexadecimal.

	assetLogger.Debug("Calculated PDF SHA256", "sha256", fileHash, "bytes", downloadedBytes) // Log the checksum.

	preferredFileName, nameErr := determinePDFFileName(assetLogger, response, assetURL) // Determine the filename.
	if nameErr != nil {                                                                 // Check naming errors.
		return outcomeUnknown, &permanentError{cause: nameErr} // Skip assets that cannot be named.
	}

	savedFileName, wasSaved, saveErr := store.saveIfNew(assetLogger, temporaryPath, preferredFileName, fileHash) // Save the PDF unless it is a duplicate.
	if saveErr != nil {                                                                                          // Check saving errors.
		return outcomeUnknown, &permanentError{cause: saveErr} // Return the error.
	}

	if !wasSaved { // Check whether the PDF was a duplicate.
		assetLogger.Debug("Duplicate PDF skipped", "sha256", fileHash) // Log the duplicate.
		return outcomeDuplicate, nil                                   // Report the duplicate.
	}

	assetLogger.Debug("PDF saved successfully", "filename", savedFileName, "sha256", fileHash, "bytes", downloadedBytes) // Log the success.

	return outcomeSaved, nil // Report the new PDF.
}

// downloadAsset downloads one asset with retries and records the result in the statistics.
func downloadAsset(ctx context.Context, workerLogger *slog.Logger, limiter *requestRateLimiter, store *pdfStore, statistics *runStatistics, assetURL string) {
	assetLogger := workerLogger.With("assetURL", assetURL) // Tag log lines with the asset URL.

	assetLogger.Debug("Processing asset") // Log the start.

	var finalOutcome downloadOutcome // Hold the outcome of the successful attempt.

	downloadErr := runWithRetries(ctx, assetLogger, "download asset", func() error { // Run the download with retries.
		attemptOutcome, attemptErr := downloadAssetOnce(ctx, assetLogger, limiter, store, assetURL) // Perform one attempt.
		finalOutcome = attemptOutcome                                                               // Keep the outcome of this attempt.

		return attemptErr // Let the retry logic decide.
	})

	if downloadErr == nil { // Check for success.
		switch finalOutcome { // Count the outcome.
		case outcomeSaved: // Handle a newly saved PDF.
			statistics.pdfsSavedCount.Add(1) // Count the saved PDF.
		case outcomeDuplicate: // Handle an existing PDF.
			statistics.duplicatePDFsCount.Add(1) // Count the duplicate.
		}

		return // Move on to the next asset.
	}

	var statusError *httpStatusError // Prepare to inspect HTTP status failures.

	switch { // Classify the failure.
	case errors.Is(downloadErr, context.Canceled): // Handle shutdown.
		statistics.interruptedAssetsCount.Add(1)             // Count the interrupted asset.
		assetLogger.Debug("Asset abandoned due to shutdown") // Log the interruption.
	case errors.Is(downloadErr, errNotAPDF): // Handle non-PDF content.
		statistics.nonPDFAssetsCount.Add(1)                   // Count the non-PDF asset.
		assetLogger.Debug("Skipping asset that is not a PDF") // Log the skip.
	case errors.As(downloadErr, &statusError) && statusError.statusCode == http.StatusNotFound: // Handle missing assets.
		statistics.missingAssetsCount.Add(1)                          // Count the missing asset.
		assetLogger.Debug("Skipping asset that does not exist (404)") // Log the skip.
	default: // Handle every other failure.
		statistics.failedAssetsCount.Add(1)                                      // Count the failed asset.
		assetLogger.Error("Skipping asset after failures", "error", downloadErr) // Log the skip.
	}
}

// addAssetIDRange adds every URL in the configured asset ID range to the collection.
func addAssetIDRange(collection *assetURLCollection) int {
	rangeURLs := make([]string, 0, lastAssetID-firstAssetID+1) // Allocate the URL slice.

	for assetID := firstAssetID; assetID <= lastAssetID; assetID++ { // Iterate through the asset ID range.
		rangeURLs = append(rangeURLs, fmt.Sprintf(assetURLFormat, assetID)) // Build and append the URL.
	}

	return collection.addAll(rangeURLs) // Store the URLs and return how many were new.
}

// run executes the whole scrape and returns any fatal error.
func run() error {
	ctx, stopSignalHandling := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM) // Cancel the context on Ctrl+C or termination.
	defer stopSignalHandling()                                                                           // Release the signal handler on exit.

	startTime := time.Now() // Remember when the run began.

	rootLogger.Debug("Starting Tormach PDF scraper", "workers", concurrentWorkerCount, "requestInterval", minimumRequestInterval, "maxRetries", maximumRetryCount, "outputDirectory", pdfOutputDirectory) // Log the configuration.

	if mkdirErr := os.MkdirAll(pdfOutputDirectory, 0755); mkdirErr != nil { // Create the output directory.
		return fmt.Errorf("create output directory: %w", mkdirErr) // Return the error.
	}

	removeStaleTemporaryFiles(rootLogger, pdfOutputDirectory) // Delete leftovers from earlier runs.

	store := newPDFStore(pdfOutputDirectory) // Create the PDF store.

	if indexErr := store.indexExistingPDFs(rootLogger); indexErr != nil { // Load existing PDF checksums.
		return fmt.Errorf("scan existing PDFs: %w", indexErr) // Return the error.
	}

	rootLogger.Debug("Loaded existing PDF hashes", "count", store.knownHashCount()) // Log the existing PDF count.

	limiter := newRequestRateLimiter(minimumRequestInterval) // Create the shared rate limiter.
	defer limiter.stop()                                     // Release the limiter on exit.

	statistics := &runStatistics{}        // Create the statistics counters.
	collection := newAssetURLCollection() // Create the asset URL set.

	processConcurrently(ctx, "machine-pages", concurrentWorkerCount, machinePageURLs, func(ctx context.Context, workerLogger *slog.Logger, pageURL string) { // Scrape all machine pages concurrently.
		scrapeMachinePage(ctx, workerLogger, limiter, statistics, collection, pageURL) // Scrape one page.
	})

	rootLogger.Debug("Machine page scraping finished", "uniqueAssetURLs", collection.size()) // Log the scraping result.

	if ctx.Err() != nil { // Check whether shutdown was requested.
		statistics.logSummary(time.Since(startTime))                         // Log what was done so far.
		return fmt.Errorf("interrupted during page scraping: %w", ctx.Err()) // Stop the run.
	}

	newRangeURLCount := addAssetIDRange(collection) // Add the numeric asset ID range.

	rootLogger.Debug("Added asset ID range", "firstID", firstAssetID, "lastID", lastAssetID, "newURLs", newRangeURLCount) // Log the range result.

	assetURLsToDownload := collection.sortedByAssetID() // Order the URLs by numeric asset ID.

	rootLogger.Debug("Total unique asset URLs to download", "count", len(assetURLsToDownload)) // Log the download count.

	processConcurrently(ctx, "asset-downloads", concurrentWorkerCount, assetURLsToDownload, func(ctx context.Context, workerLogger *slog.Logger, assetURL string) { // Download all assets concurrently.
		downloadAsset(ctx, workerLogger, limiter, store, statistics, assetURL) // Download one asset.
	})

	statistics.logSummary(time.Since(startTime)) // Log the final statistics.

	if ctx.Err() != nil { // Check whether shutdown cut the run short.
		return fmt.Errorf("interrupted during downloads: %w", ctx.Err()) // Report the interruption.
	}

	rootLogger.Debug("Tormach PDF scraping completed") // Log completion.

	return nil // Report success.
}

// Main application entry point.
func main() {
	if runErr := run(); runErr != nil { // Run the scraper.
		rootLogger.Error("Scraper stopped with an error", "error", runErr) // Log the fatal error.
		os.Exit(1)                                                         // Exit with a failure code.
	}
}
