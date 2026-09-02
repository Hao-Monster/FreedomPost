// Package domain defines all business entities and the Repository interface.
// This package has no dependencies on any infrastructure (no pgx, no redis, no http).
package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// ─── Post ────────────────────────────────────────────────────────────────────

type PostVisibility string

const (
	VisibilityPublic  PostVisibility = "public"
	VisibilityPaid    PostVisibility = "paid"
	VisibilityPrivate PostVisibility = "private"
)

type Post struct {
	ID              string         `json:"id"`
	Slug            string         `json:"slug"`
	Title           string         `json:"title"`
	ContentMarkdown string         `json:"contentMarkdown"`
	Markdown        string         `json:"markdown"`
	ContentHTML     string         `json:"contentHtml"`
	SearchText      string         `json:"searchText,omitempty"`
	Excerpt         string         `json:"excerpt,omitempty"`
	Visibility      PostVisibility `json:"visibility"`
	PriceCents      int            `json:"priceCents"`
	Currency        string         `json:"currency"`
	ViewCount       int            `json:"viewCount"`
	CommentCount    int            `json:"commentCount"`
	AttachmentCount int            `json:"attachmentCount"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

// PostSummary is the lightweight list item for public post listing.
type PostSummary struct {
	Slug         string         `json:"slug"`
	Title        string         `json:"title"`
	Visibility   PostVisibility `json:"visibility"`
	PriceCents   int            `json:"priceCents,omitempty"`
	Currency     string         `json:"currency,omitempty"`
	Excerpt      string         `json:"excerpt,omitempty"`
	ViewCount    int            `json:"viewCount"`
	CommentCount int            `json:"commentCount"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
}

// SearchDocument is the shape sent to /api/search-index, aligned with
// @freedompost/shared SearchDocument.
type SearchDocument struct {
	ID         string    `json:"id"`
	Slug       string    `json:"slug"`
	Title      string    `json:"title"`
	Body       string    `json:"body"` // plain text for search (from search_text column)
	SearchText string    `json:"-"`    // internal alias, not serialized
	Excerpt    string    `json:"excerpt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type CreatePostInput struct {
	Title           string
	Visibility      PostVisibility
	PriceCents      int
	Currency        string
	ContentMarkdown string
	ContentHTML     string
	SearchText      string
	Excerpt         string
}

type UpdatePostInput struct {
	ID              string
	Title           string
	Slug            string // new slug if changed
	Visibility      PostVisibility
	PriceCents      int
	Currency        string
	ContentMarkdown string
	ContentHTML     string
	SearchText      string
	Excerpt         string
}

type RecordViewInput struct {
	PostSlug        string
	ViewDate        string // YYYY-MM-DD
	VisitorKey      string
	IPHash          string
	FingerprintHash string
	LocalIDHash     string
}

type RecordViewResult struct {
	Counted   bool `json:"counted"`
	ViewCount int  `json:"viewCount"`
}

// ─── Comment ─────────────────────────────────────────────────────────────────

type Comment struct {
	ID          string              `json:"id"`
	PostSlug    string              `json:"postSlug"`
	ParentID    *string             `json:"parentId"`
	RootID      *string             `json:"rootId"`
	Depth       int                 `json:"depth"`
	Path        string              `json:"path"`
	Username    string              `json:"username"`
	Content     string              `json:"content"`
	Attachments []CommentAttachment `json:"attachments"`
	CreatedAt   time.Time           `json:"createdAt"`
}

type CommentAttachment struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	MimeType        string `json:"mimeType"`
	SizeBytes       int64  `json:"sizeBytes"`
	URL             string `json:"url"`
	StorageProvider string `json:"storageProvider,omitempty"`
	StorageKey      string `json:"storageKey,omitempty"`
	StoredFilename  string `json:"storedFilename,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
}

type CreateCommentInput struct {
	PostSlug        string
	ParentID        string // empty = top-level
	Username        string
	Content         string
	IPHash          string
	FingerprintHash string
	LocalIDHash     string
	Attachments     []PendingAttachment
}

// PendingAttachment is an upload that has already been stored and is being
// linked to a new comment.
type PendingAttachment struct {
	ID              string
	ClaimToken      string
	Name            string
	MimeType        string
	SizeBytes       int64
	URL             string
	StorageProvider string
	StorageKey      string
	StoredFilename  string
	SHA256          string
}

// ─── Attachment ──────────────────────────────────────────────────────────────

type Attachment struct {
	ID               string    `json:"id"`
	OwnerType        string    `json:"ownerType"`
	OwnerID          *string   `json:"ownerId"`
	OriginalFilename string    `json:"originalFilename"`
	StoredFilename   string    `json:"storedFilename"`
	StorageProvider  string    `json:"storageProvider"`
	StorageKey       string    `json:"storageKey"`
	PublicURL        string    `json:"publicUrl"`
	MimeType         string    `json:"mimeType"`
	DetectedMime     string    `json:"detectedMimeType,omitempty"`
	SizeBytes        int64     `json:"sizeBytes"`
	Width            *int      `json:"width,omitempty"`
	Height           *int      `json:"height,omitempty"`
	SHA256           string    `json:"sha256,omitempty"`
	ClaimToken       string    `json:"claimToken,omitempty"`
	ClaimTokenHash   string    `json:"-"`
	UploaderType     string    `json:"uploaderType,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
}

// ─── Product ─────────────────────────────────────────────────────────────────

type Product struct {
	ID              string    `json:"id"`
	Slug            string    `json:"slug"`
	Title           string    `json:"title"`
	Summary         string    `json:"summary"`
	Description     string    `json:"description"`
	Category        string    `json:"category"`
	CoverURL        string    `json:"coverUrl"`
	LinkURL         string    `json:"linkUrl"`
	PriceCents      int       `json:"priceCents"`
	CompareAtCents  *int      `json:"compareAtCents"`
	Currency        string    `json:"currency"`
	CommissionCents int       `json:"commissionCents"`
	Stock           int       `json:"stock"`
	SoldCount       int       `json:"soldCount"`
	Status          string    `json:"status"`
	SortOrder       int       `json:"sortOrder"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// AffiliateProductView is the product as seen by an affiliate, including
// markup calculation. Aligns with @freedompost/shared AffiliateProductView
// and the fix in commit 2f1e598.
type AffiliateProductView struct {
	Product
	MarkupPercent         int `json:"markupPercent"`
	CustomerPriceCents    int `json:"customerPriceCents"`
	BaseCommissionCents   int `json:"baseCommissionCents"`   // from product.CommissionCents
	MarkupCommissionCents int `json:"markupCommissionCents"` // from markup
	CommissionCents       int `json:"commissionCents"`       // total = base + markup
}

const MaxAffiliateMarkupPercent = 1000

func ValidAffiliateMarkupPercent(value int) bool {
	return value >= 0 && value <= MaxAffiliateMarkupPercent
}

// BuildAffiliateProductView computes server-authoritative customer price and
// affiliate earnings. Matches TypeScript buildAffiliateProductView() exactly
// (including Math.round behaviour: (a*b + 50) / 100 for rounding half-up).
func BuildAffiliateProductView(product Product, markupPercent int) AffiliateProductView {
	customerPriceCents := (product.PriceCents*(100+markupPercent) + 50) / 100
	markupCommissionCents := customerPriceCents - product.PriceCents
	baseCommissionCents := product.CommissionCents
	return AffiliateProductView{
		Product:               product,
		MarkupPercent:         markupPercent,
		CustomerPriceCents:    customerPriceCents,
		BaseCommissionCents:   baseCommissionCents,
		MarkupCommissionCents: markupCommissionCents,
		CommissionCents:       baseCommissionCents + markupCommissionCents,
	}
}

type ProductInput struct {
	Title           string `json:"title"`
	Summary         string `json:"summary"`
	Description     string `json:"description"`
	Category        string `json:"category"`
	CoverURL        string `json:"coverUrl"`
	LinkURL         string `json:"linkUrl"`
	PriceCents      int    `json:"priceCents"`
	CompareAtCents  *int   `json:"compareAtCents"`
	Currency        string `json:"currency"`
	CommissionCents int    `json:"commissionCents"`
	Stock           int    `json:"stock"`
	SoldCount       int    `json:"soldCount"`
	Status          string `json:"status"`
	SortOrder       int    `json:"sortOrder"`
	priceCentsSet   bool
	compareAtSet    bool
	stockSet        bool
	sortOrderSet    bool
}

// HasRequiredJSONFields distinguishes omitted numeric fields from their valid
// zero values. Admin create/update requests must carry the complete product
// contract so omission cannot silently turn unlimited inventory into sold out.
func (p ProductInput) HasRequiredJSONFields() bool {
	return len(p.MissingRequiredJSONFields()) == 0
}

// MissingRequiredJSONFields reports numeric fields whose omission would be
// indistinguishable from a legitimate zero value after JSON decoding.
func (p ProductInput) MissingRequiredJSONFields() []string {
	missing := make([]string, 0, 4)
	if !p.priceCentsSet {
		missing = append(missing, "priceCents")
	}
	if !p.compareAtSet {
		missing = append(missing, "compareAtCents")
	}
	if !p.stockSet {
		missing = append(missing, "stock")
	}
	if !p.sortOrderSet {
		missing = append(missing, "sortOrder")
	}
	return missing
}

// UnmarshalJSON preserves the legacy imageUrl input while keeping strict
// unknown-field rejection for the public admin contract.
func (p *ProductInput) UnmarshalJSON(data []byte) error {
	var wire struct {
		Title           string `json:"title"`
		Summary         string `json:"summary"`
		Description     string `json:"description"`
		Category        string `json:"category"`
		CoverURL        string `json:"coverUrl"`
		ImageURL        string `json:"imageUrl"`
		LinkURL         string `json:"linkUrl"`
		PriceCents      *int   `json:"priceCents"`
		CompareAtCents  *int   `json:"compareAtCents"`
		Currency        string `json:"currency"`
		CommissionCents int    `json:"commissionCents"`
		Stock           *int   `json:"stock"`
		SoldCount       int    `json:"soldCount"`
		Status          string `json:"status"`
		SortOrder       *int   `json:"sortOrder"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	coverURL := wire.CoverURL
	if coverURL == "" {
		coverURL = wire.ImageURL
	}
	priceCents := 0
	if wire.PriceCents != nil {
		priceCents = *wire.PriceCents
	}
	stock := 0
	if wire.Stock != nil {
		stock = *wire.Stock
	}
	sortOrder := 0
	if wire.SortOrder != nil {
		sortOrder = *wire.SortOrder
	}
	_, priceCentsSet := fields["priceCents"]
	_, compareAtSet := fields["compareAtCents"]
	_, stockSet := fields["stock"]
	_, sortOrderSet := fields["sortOrder"]
	*p = ProductInput{
		Title: wire.Title, Summary: wire.Summary, Description: wire.Description,
		Category: wire.Category, CoverURL: coverURL, LinkURL: wire.LinkURL,
		PriceCents: priceCents, CompareAtCents: wire.CompareAtCents,
		Currency: wire.Currency, CommissionCents: wire.CommissionCents,
		Stock: stock, SoldCount: wire.SoldCount, Status: wire.Status,
		SortOrder: sortOrder, priceCentsSet: priceCentsSet,
		compareAtSet: compareAtSet, stockSet: stockSet, sortOrderSet: sortOrderSet,
	}
	return nil
}

// ─── Tool ────────────────────────────────────────────────────────────────────

type Tool struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary,omitempty"`
	Description string    `json:"description"`
	Category    string    `json:"category,omitempty"`
	URL         string    `json:"url"`
	LinkURL     string    `json:"linkUrl"`
	CoverURL    string    `json:"coverUrl"`
	IconURL     string    `json:"iconUrl"`
	Tags        []string  `json:"tags,omitempty"`
	Status      string    `json:"status"`
	SortOrder   int       `json:"sortOrder"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type ToolInput struct {
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	IconURL     string   `json:"iconUrl"`
	LinkURL     string   `json:"linkUrl"`
	Tags        []string `json:"tags"`
	Status      string   `json:"status"`
	SortOrder   int      `json:"sortOrder"`
}

// ─── Affiliate ───────────────────────────────────────────────────────────────

type AffiliateStatus string

const (
	AffiliateStatusActive   AffiliateStatus = "active"
	AffiliateStatusDisabled AffiliateStatus = "disabled"
)

type Affiliate struct {
	ID                   string          `json:"id"`
	WechatID             string          `json:"wechatId"`
	PasswordHash         string          `json:"-"`
	CredentialVersion    int             `json:"-"`
	Status               AffiliateStatus `json:"status"`
	DefaultMarkupPercent int             `json:"defaultMarkupPercent"`
	CreatedAt            time.Time       `json:"createdAt"`
	UpdatedAt            time.Time       `json:"updatedAt"`
}

type AffiliateListItem struct {
	Affiliate
	TotalClicks  int `json:"totalClicks"`
	UniqueClicks int `json:"uniqueClicks"`
	OrderCount   int `json:"orderCount"`
}

type AffiliateDashboard struct {
	Affiliate              Affiliate        `json:"affiliate"`
	TotalClicks            int              `json:"totalClicks"`
	UniqueClicks           int              `json:"uniqueClicks"`
	CompletedOrders        int              `json:"completedOrders"`
	PendingCommissionCents int              `json:"pendingCommissionCents"`
	PaidCommissionCents    int              `json:"paidCommissionCents"`
	Orders                 []AffiliateOrder `json:"orders"`
}

type AffiliateOrder struct {
	ID                    string     `json:"id"`
	OrderCode             string     `json:"orderCode"`
	AffiliateID           string     `json:"affiliateId"`
	AffiliateWechatID     string     `json:"affiliateWechatId"`
	ProductID             string     `json:"productId"`
	ProductTitle          string     `json:"productTitle"`
	PriceCents            int        `json:"priceCents"`
	CustomerPriceCents    int        `json:"customerPriceCents"`
	CommissionCents       int        `json:"commissionCents"`
	BaseCommissionCents   int        `json:"baseCommissionCents"`
	MarkupCommissionCents int        `json:"markupCommissionCents"`
	Currency              string     `json:"currency"`
	Status                string     `json:"status"`
	OrderStatus           string     `json:"orderStatus"`
	CommissionStatus      string     `json:"commissionStatus"`
	Notes                 string     `json:"notes,omitempty"`
	CompletedAt           *time.Time `json:"completedAt"`
	CommissionPaidAt      *time.Time `json:"commissionPaidAt"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`
}

type CreateOrderInput struct {
	AffiliateWechatID string
	ProductSlug       string
}

// ─── Admin ───────────────────────────────────────────────────────────────────

type AdminSession struct {
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"createdAt"`
}

type AffiliateSession struct {
	AffiliateID       string    `json:"affiliateId"`
	WechatID          string    `json:"wechatId"`
	CredentialVersion int       `json:"credentialVersion"`
	CreatedAt         time.Time `json:"createdAt"`
}

// ─── Benefit ─────────────────────────────────────────────────────────────────

type BenefitCampaign struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	StartsAt string `json:"startsAt,omitempty"` // RFC3339 or ""
	EndsAt   string `json:"endsAt,omitempty"`   // RFC3339 or ""
}

type BenefitClaim struct {
	ID                 string    `json:"id"`
	CampaignID         string    `json:"campaignId"`
	ExternalClaimID    string    `json:"-"`
	BrowserKeyHash     string    `json:"-"`
	NetworkKeyHash     string    `json:"-"`
	Status             string    `json:"status"` // pending|provisioning|ready|revoked|expired|failed
	OpusUserID         string    `json:"-"`
	OpusDeviceID       string    `json:"-"`
	SubscriptionURLEnc string    `json:"-"` // AES-GCM encrypted
	ExpiresAt          string    `json:"expiresAt,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type CreateBenefitClaimInput struct {
	CampaignID      string
	ExternalClaimID string
	BrowserKeyHash  string
	NetworkKeyHash  string
}

type CreateBenefitClaimResult struct {
	Claim   *BenefitClaim
	Created bool // false = already existed (idempotent)
}

type CompleteBenefitClaimInput struct {
	OpusUserID         string
	OpusDeviceID       string
	SubscriptionURLEnc string
	ExpiresAt          string // RFC3339
}

// ─── Reader Account (from paid-access, read-only proxy) ──────────────────────

type ReaderAccount struct {
	ID        string    `json:"id"`
	LoginName string    `json:"loginName"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

// ─── Article Order (paid-access) ─────────────────────────────────────────────

type ArticleOrder struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"accountId"`
	PostID      string    `json:"postId"`
	AmountCents int       `json:"amountCents"`
	Currency    string    `json:"currency"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
}
