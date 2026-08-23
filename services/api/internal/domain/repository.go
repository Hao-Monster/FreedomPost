package domain

import "context"

// Repository is the central data access interface. All infrastructure
// (PostgreSQL) implementations must satisfy this interface.
// The interface is designed to be testable with in-memory implementations.
type Repository interface {
	// ─── Posts ──────────────────────────────────────────────────────────────
	ListPosts(ctx context.Context) ([]Post, error)
	ListPostSummaries(ctx context.Context) ([]PostSummary, error)
	GetSearchDocuments(ctx context.Context) ([]SearchDocument, error)
	GetPostBySlug(ctx context.Context, slug string) (*Post, error)
	GetPostByID(ctx context.Context, id string) (*Post, error)
	CreatePost(ctx context.Context, input CreatePostInput) (*Post, error)
	UpdatePost(ctx context.Context, input UpdatePostInput) (*Post, error)
	DeletePost(ctx context.Context, id string) (bool, error)
	RecordView(ctx context.Context, input RecordViewInput) (*RecordViewResult, error)

	// ─── Comments ───────────────────────────────────────────────────────────
	ListComments(ctx context.Context, postSlug string) ([]Comment, error)
	CreateComment(ctx context.Context, input CreateCommentInput) (*Comment, error)

	// ─── Attachments ────────────────────────────────────────────────────────
	CreateAttachment(ctx context.Context, a Attachment) (*Attachment, error)
	DeleteAttachmentsByIDs(ctx context.Context, ids []string) error

	// ─── Products ───────────────────────────────────────────────────────────
	ListProducts(ctx context.Context, publishedOnly bool) ([]Product, error)
	GetProductBySlug(ctx context.Context, slug string) (*Product, error)
	GetProductByID(ctx context.Context, id string) (*Product, error)
	CreateProduct(ctx context.Context, input ProductInput) (*Product, error)
	UpdateProduct(ctx context.Context, id string, input ProductInput) (*Product, error)
	DeleteProduct(ctx context.Context, id string) (bool, error)

	// ─── Tools ──────────────────────────────────────────────────────────────
	ListTools(ctx context.Context, publishedOnly bool) ([]Tool, error)
	GetToolByID(ctx context.Context, id string) (*Tool, error)
	CreateTool(ctx context.Context, input ToolInput) (*Tool, error)
	UpdateTool(ctx context.Context, id string, input ToolInput) (*Tool, error)
	DeleteTool(ctx context.Context, id string) (bool, error)

	// ─── Affiliates ─────────────────────────────────────────────────────────
	ListAffiliates(ctx context.Context) ([]AffiliateListItem, error)
	GetAffiliateByWechatID(ctx context.Context, wechatID string) (*Affiliate, error)
	GetAffiliateByID(ctx context.Context, id string) (*Affiliate, error)
	CreateAffiliate(ctx context.Context, wechatID, passwordHash string) (*Affiliate, error)
	UpdateAffiliateStatus(ctx context.Context, id string, status AffiliateStatus) (bool, error)
	UpdateAffiliatePassword(ctx context.Context, id string, passwordHash string) (bool, error)
	ListAffiliateProducts(ctx context.Context, affiliateID string) ([]AffiliateProductView, error)
	SetAffiliateMarkup(ctx context.Context, affiliateID string, productIDs []string, markupPercent int) error
	GetAffiliateDashboard(ctx context.Context, affiliateID string) (*AffiliateDashboard, error)
	RecordAffiliateClick(ctx context.Context, wechatID, visitorKey, path string) (accepted bool, isUnique bool, err error)

	// ─── Orders ─────────────────────────────────────────────────────────────
	CreateAffiliateOrder(ctx context.Context, input CreateOrderInput) (*AffiliateOrder, error)
	ListAffiliateOrders(ctx context.Context) ([]AffiliateOrder, error)
	UpdateAffiliateOrderStatus(ctx context.Context, id, status, notes string) (bool, error)

	// ─── Reader Accounts (proxied from paid-access) ──────────────────────────
	// These are managed by paid-access; admin API proxies via HTTP.
	// No direct DB access for reader accounts in this service.

	// ─── Benefit ────────────────────────────────────────────────────────────
	GetBenefitCampaign(ctx context.Context, id string) (*BenefitCampaign, error)
	CreateBenefitClaim(ctx context.Context, input CreateBenefitClaimInput) (*CreateBenefitClaimResult, error)
	GetBenefitClaimByBrowserKey(ctx context.Context, campaignID, browserKeyHash string) (*BenefitClaim, error)
	GetBenefitClaimByID(ctx context.Context, id string) (*BenefitClaim, error)
	CountBenefitClaimsByNetworkSince(ctx context.Context, campaignID, networkKeyHash, since string) (int, error)
	BeginBenefitProvisioning(ctx context.Context, claimID string) (*BenefitClaim, error)
	CompleteBenefitClaim(ctx context.Context, claimID string, input CompleteBenefitClaimInput) (*BenefitClaim, error)
	FailBenefitClaim(ctx context.Context, claimID, errorCode string) error
	RecoverStaleBenefitProvisioning(ctx context.Context, claimID, olderThan string) (*BenefitClaim, error)

	// ─── Lifecycle ──────────────────────────────────────────────────────────
	Ping(ctx context.Context) error
	Close()
}
