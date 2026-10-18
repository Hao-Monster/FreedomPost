import {
  bigint,
  bigserial,
  boolean,
  check,
  date,
  index,
  integer,
  jsonb,
  pgTable,
  primaryKey,
  text,
  timestamp,
  unique,
  uuid,
  varchar
} from "drizzle-orm/pg-core";
import type { AnyPgColumn } from "drizzle-orm/pg-core";
import { sql } from "drizzle-orm";

export const POST_INITIAL_VIEW_COUNT = 100;
export const POST_VIEW_INCREMENT = 100;

export const posts = pgTable(
  "posts",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    slug: varchar("slug", { length: 32 }).notNull().unique(),
    title: text("title").notNull(),
    contentJson: jsonb("content_json").notNull(),
    contentMarkdown: text("content_markdown"),
    contentHtml: text("content_html").notNull(),
    searchText: text("search_text").notNull(),
    excerpt: text("excerpt"),
    visibility: varchar("visibility", { length: 16 }).notNull().default("public"),
    priceCents: integer("price_cents").notNull().default(0),
    currency: varchar("currency", { length: 8 }).notNull().default("CNY"),
    seoTitle: text("seo_title"),
    seoDescription: text("seo_description"),
    viewCount: bigint("view_count", { mode: "number" }).notNull().default(POST_INITIAL_VIEW_COUNT),
    commentCount: bigint("comment_count", { mode: "number" }).notNull().default(0),
    attachmentCount: bigint("attachment_count", { mode: "number" }).notNull().default(0),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    createdAtIdx: index("idx_posts_created_at").on(table.createdAt),
    updatedAtIdx: index("idx_posts_updated_at").on(table.updatedAt)
  })
);

export const postSlugAliases = pgTable(
  "post_slug_aliases",
  {
    slug: varchar("slug", { length: 32 }).primaryKey(),
    postId: uuid("post_id").notNull().references(() => posts.id, { onDelete: "cascade" }),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    postIdIdx: index("idx_post_slug_aliases_post_id").on(table.postId)
  })
);

export const products = pgTable(
  "products",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    slug: varchar("slug", { length: 64 }).notNull().unique(),
    title: text("title").notNull(),
    summary: text("summary").notNull().default(""),
    description: text("description").notNull().default(""),
    category: varchar("category", { length: 32 }).notNull().default("other"),
    priceCents: integer("price_cents").notNull().default(0),
    commissionCents: integer("commission_cents").notNull().default(0),
    compareAtCents: integer("compare_at_cents"),
    currency: varchar("currency", { length: 8 }).notNull().default("CNY"),
    stock: integer("stock").notNull().default(-1),
    soldCount: integer("sold_count").notNull().default(0),
    coverUrl: text("cover_url"),
    status: varchar("status", { length: 16 }).notNull().default("draft"),
    sortOrder: integer("sort_order").notNull().default(0),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    createdAtIdx: index("idx_products_created_at").on(table.createdAt),
    statusIdx: index("idx_products_status_sort").on(table.status, table.sortOrder)
  })
);

export const tools = pgTable(
  "tools",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    slug: varchar("slug", { length: 64 }).notNull().unique(),
    title: text("title").notNull(),
    summary: text("summary").notNull().default(""),
    description: text("description").notNull().default(""),
    category: varchar("category", { length: 32 }).notNull().default("other"),
    url: text("url").notNull(),
    coverUrl: text("cover_url"),
    status: varchar("status", { length: 16 }).notNull().default("draft"),
    sortOrder: integer("sort_order").notNull().default(0),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    createdAtIdx: index("idx_tools_created_at").on(table.createdAt),
    statusIdx: index("idx_tools_status_sort").on(table.status, table.sortOrder)
  })
);

export const affiliates = pgTable(
  "affiliates",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    wechatId: varchar("wechat_id", { length: 32 }).notNull().unique(),
    passwordHash: text("password_hash").notNull(),
    credentialVersion: integer("credential_version").notNull().default(1),
    defaultMarkupPercent: integer("default_markup_percent").notNull().default(0),
    status: varchar("status", { length: 16 }).notNull().default("active"),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    statusIdx: index("idx_affiliates_status").on(table.status)
  })
);

export const affiliateProductMarkups = pgTable(
  "affiliate_product_markups",
  {
    affiliateId: uuid("affiliate_id")
      .notNull()
      .references(() => affiliates.id, { onDelete: "cascade" }),
    productId: uuid("product_id")
      .notNull()
      .references(() => products.id, { onDelete: "cascade" }),
    markupPercent: integer("markup_percent").notNull().default(0),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    primaryKey: primaryKey({ columns: [table.affiliateId, table.productId] }),
    affiliateIdx: index("idx_affiliate_product_markups_affiliate").on(table.affiliateId),
    productIdx: index("idx_affiliate_product_markups_product").on(table.productId)
  })
);

export const affiliateClicks = pgTable(
  "affiliate_clicks",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    affiliateId: uuid("affiliate_id")
      .notNull()
      .references(() => affiliates.id, { onDelete: "cascade" }),
    visitorKey: varchar("visitor_key", { length: 128 }).notNull(),
    path: text("path").notNull().default("/market/"),
    isUnique: integer("is_unique").notNull().default(0),
    clickedAt: timestamp("clicked_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    affiliateTimeIdx: index("idx_affiliate_clicks_affiliate_time").on(table.affiliateId, table.clickedAt),
    visitorTimeIdx: index("idx_affiliate_clicks_visitor_time").on(table.affiliateId, table.visitorKey, table.clickedAt)
  })
);

export const affiliateOrders = pgTable(
  "affiliate_orders",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    orderCode: varchar("order_code", { length: 16 }).notNull().unique(),
    affiliateId: uuid("affiliate_id")
      .notNull()
      .references(() => affiliates.id, { onDelete: "restrict" }),
    productId: uuid("product_id").references(() => products.id, { onDelete: "set null" }),
    productTitle: text("product_title").notNull(),
    priceCents: integer("price_cents").notNull(),
    commissionCents: integer("commission_cents").notNull(),
    baseCommissionCents: integer("base_commission_cents").notNull().default(0),
    markupCommissionCents: integer("markup_commission_cents").notNull().default(0),
    currency: varchar("currency", { length: 8 }).notNull().default("CNY"),
    orderStatus: varchar("order_status", { length: 16 }).notNull().default("pending"),
    commissionStatus: varchar("commission_status", { length: 16 }).notNull().default("not_due"),
    stockReserved: boolean("stock_reserved").notNull().default(false),
    notes: text("notes").notNull().default(""),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow(),
    completedAt: timestamp("completed_at", { withTimezone: true }),
    commissionPaidAt: timestamp("commission_paid_at", { withTimezone: true })
  },
  (table) => ({
    affiliateIdx: index("idx_affiliate_orders_affiliate").on(table.affiliateId, table.createdAt),
    statusIdx: index("idx_affiliate_orders_status").on(table.orderStatus, table.commissionStatus)
  })
);

export const affiliateOrderRequests = pgTable(
  "affiliate_order_requests",
  {
    ownerHash: varchar("owner_hash", { length: 64 }).notNull(),
    requestKey: uuid("request_key").notNull(),
    inputHash: varchar("input_hash", { length: 64 }).notNull(),
    orderId: uuid("order_id")
      .notNull()
      .unique()
      .references(() => affiliateOrders.id, { onDelete: "restrict" }),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    primaryKey: primaryKey({ columns: [table.ownerHash, table.requestKey] })
  })
);

export const affiliateOrderSupportDeliveries = pgTable(
  "affiliate_order_support_deliveries",
  {
    orderId: uuid("order_id")
      .primaryKey()
      .references(() => affiliateOrders.id, { onDelete: "restrict" }),
    status: varchar("status", { length: 24 }).notNull().default("awaiting_session"),
    bindingId: varchar("binding_id", { length: 256 }),
    messageId: bigint("message_id", { mode: "number" }),
    conversationId: bigint("conversation_id", { mode: "number" }),
    attempts: integer("attempts").notNull().default(0),
    errorCode: varchar("error_code", { length: 64 }).notNull().default(""),
    leaseUntil: timestamp("lease_until", { withTimezone: true }),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow()
  }
);

export const postArtifacts = pgTable(
  "post_artifacts",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    postId: uuid("post_id")
      .notNull()
      .references(() => posts.id, { onDelete: "cascade" }),
    htmlHash: varchar("html_hash", { length: 64 }).notNull(),
    tocJson: jsonb("toc_json").notNull(),
    assetManifest: jsonb("asset_manifest").notNull().default({}),
    generatedAt: timestamp("generated_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    postIdIdx: index("idx_post_artifacts_post_id").on(table.postId)
  })
);

export const comments = pgTable(
  "comments",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    postId: uuid("post_id")
      .notNull()
      .references(() => posts.id, { onDelete: "cascade" }),
    parentId: uuid("parent_id").references((): AnyPgColumn => comments.id, {
      onDelete: "cascade"
    }),
    rootId: uuid("root_id").references((): AnyPgColumn => comments.id, {
      onDelete: "cascade"
    }),
    depth: integer("depth").notNull().default(0),
    path: text("path").notNull(),
    username: text("username").notNull(),
    fingerprintHash: varchar("fingerprint_hash", { length: 128 }),
    localIdHash: varchar("local_id_hash", { length: 128 }),
    ipHash: varchar("ip_hash", { length: 128 }).notNull(),
    content: text("content"),
    attachmentCount: integer("attachment_count").notNull().default(0),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    postRootIdx: index("idx_comments_post_root").on(table.postId, table.rootId, table.path),
    postCreatedIdx: index("idx_comments_post_created").on(table.postId, table.createdAt),
    parentIdx: index("idx_comments_parent").on(table.parentId)
  })
);

export const attachments = pgTable(
  "attachments",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    ownerType: varchar("owner_type", { length: 32 }).notNull(),
    ownerId: uuid("owner_id"),
    uploaderType: varchar("uploader_type", { length: 32 }).notNull(),
    originalFilename: text("original_filename").notNull(),
    storedFilename: text("stored_filename").notNull(),
    storageProvider: varchar("storage_provider", { length: 32 }).notNull(),
    storageKey: text("storage_key").notNull(),
    publicUrl: text("public_url").notNull(),
    mimeType: text("mime_type").notNull(),
    detectedMimeType: text("detected_mime_type"),
    sizeBytes: bigint("size_bytes", { mode: "number" }).notNull(),
    width: integer("width"),
    height: integer("height"),
    sha256: varchar("sha256", { length: 128 }),
    claimTokenHash: varchar("claim_token_hash", { length: 64 }),
    pendingUntil: timestamp("pending_until", { withTimezone: true }),
    sourceHost: text("source_host"),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    ownerIdx: index("idx_attachments_owner").on(table.ownerType, table.ownerId),
    hashIdx: index("idx_attachments_hash").on(table.sha256)
  })
);

export const postViews = pgTable(
  "post_views",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    postId: uuid("post_id")
      .notNull()
      .references(() => posts.id, { onDelete: "cascade" }),
    viewDate: date("view_date").notNull(),
    visitorKey: varchar("visitor_key", { length: 128 }).notNull(),
    ipHash: varchar("ip_hash", { length: 128 }),
    fingerprintHash: varchar("fingerprint_hash", { length: 128 }),
    localIdHash: varchar("local_id_hash", { length: 128 }),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    uniqueVisitor: unique("uniq_post_views_daily_visitor").on(
      table.postId,
      table.viewDate,
      table.visitorKey
    )
  })
);

export const admins = pgTable("admins", {
  id: uuid("id").primaryKey().defaultRandom(),
  username: text("username").notNull().unique(),
  passwordHash: text("password_hash").notNull(),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow()
});

export const adminSessions = pgTable("admin_sessions", {
  id: uuid("id").primaryKey().defaultRandom(),
  adminId: uuid("admin_id")
    .notNull()
    .references(() => admins.id, { onDelete: "cascade" }),
  tokenHash: varchar("token_hash", { length: 128 }).notNull().unique(),
  userAgentHash: varchar("user_agent_hash", { length: 128 }),
  ipHash: varchar("ip_hash", { length: 128 }),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  lastUsedAt: timestamp("last_used_at", { withTimezone: true }).notNull().defaultNow(),
  revokedAt: timestamp("revoked_at", { withTimezone: true })
});

export const readerAccounts = pgTable("reader_accounts", {
  id: uuid("id").primaryKey().defaultRandom(),
  loginName: text("login_name").notNull(),
  normalizedLogin: text("normalized_login").notNull().unique(),
  passwordHash: text("password_hash").notNull(),
  credentialVersion: integer("credential_version").notNull().default(1),
  status: varchar("status", { length: 16 }).notNull().default("active"),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow()
});

export const readerSessions = pgTable("reader_sessions", {
  id: uuid("id").primaryKey().defaultRandom(),
  accountId: uuid("account_id")
    .notNull()
    .references(() => readerAccounts.id, { onDelete: "cascade" }),
  tokenHash: varchar("token_hash", { length: 64 }).notNull().unique(),
  credentialVersion: integer("credential_version").notNull(),
  userAgentHash: varchar("user_agent_hash", { length: 64 }),
  ipHash: varchar("ip_hash", { length: 64 }),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  lastSeenAt: timestamp("last_seen_at", { withTimezone: true }).notNull().defaultNow(),
  revokedAt: timestamp("revoked_at", { withTimezone: true })
});

export const articleOrders = pgTable(
  "article_orders",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    orderCode: varchar("order_code", { length: 24 }).notNull().unique(),
    accountId: uuid("account_id")
      .notNull()
      .references(() => readerAccounts.id, { onDelete: "restrict" }),
    postId: uuid("post_id")
      .notNull()
      .references(() => posts.id, { onDelete: "restrict" }),
    postTitle: text("post_title").notNull(),
    priceCents: integer("price_cents").notNull(),
    currency: varchar("currency", { length: 8 }).notNull().default("CNY"),
    status: varchar("status", { length: 16 }).notNull().default("pending"),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow(),
    completedAt: timestamp("completed_at", { withTimezone: true }),
    completedBy: text("completed_by")
  },
  (table) => ({
    accountCreatedIdx: index("idx_article_orders_account_created").on(table.accountId, table.createdAt),
    statusCreatedIdx: index("idx_article_orders_status_created").on(table.status, table.createdAt)
  })
);

export const articleEntitlements = pgTable(
  "article_entitlements",
  {
    accountId: uuid("account_id")
      .notNull()
      .references(() => readerAccounts.id, { onDelete: "cascade" }),
    postId: uuid("post_id")
      .notNull()
      .references(() => posts.id, { onDelete: "cascade" }),
    sourceOrderId: uuid("source_order_id").references(() => articleOrders.id, { onDelete: "set null" }),
    grantedAt: timestamp("granted_at", { withTimezone: true }).notNull().defaultNow(),
    grantedBy: text("granted_by").notNull()
  },
  (table) => ({
    primaryKey: primaryKey({ columns: [table.accountId, table.postId] })
  })
);

export const articleAccessAuditLog = pgTable(
  "article_access_audit_log",
  {
    id: bigserial("id", { mode: "number" }).primaryKey(),
    actorType: varchar("actor_type", { length: 16 }).notNull(),
    actorId: text("actor_id"),
    action: varchar("action", { length: 64 }).notNull(),
    targetType: varchar("target_type", { length: 32 }).notNull(),
    targetId: text("target_id").notNull(),
    metadata: jsonb("metadata").notNull().default({}),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    targetIdx: index("idx_article_access_audit_target").on(table.targetType, table.targetId, table.createdAt)
  })
);

export const commentRateLimits = pgTable(
  "comment_rate_limits",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    postId: uuid("post_id")
      .notNull()
      .references(() => posts.id, { onDelete: "cascade" }),
    subjectType: varchar("subject_type", { length: 32 }).notNull(),
    subjectHash: varchar("subject_hash", { length: 128 }).notNull(),
    windowType: varchar("window_type", { length: 32 }).notNull(),
    windowStart: timestamp("window_start", { withTimezone: true }).notNull(),
    count: integer("count").notNull().default(0),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    uniqueWindow: unique("uniq_comment_rate_limit_window").on(
      table.postId,
      table.subjectType,
      table.subjectHash,
      table.windowType,
      table.windowStart
    )
  })
);

export const benefitCampaigns = pgTable("benefit_campaigns", {
  id: varchar("id", { length: 64 }).primaryKey(),
  name: text("name").notNull(),
  enabled: boolean("enabled").notNull().default(false),
  startsAt: timestamp("starts_at", { withTimezone: true }),
  endsAt: timestamp("ends_at", { withTimezone: true }),
  createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
  updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow()
});

export const benefitClaims = pgTable(
  "benefit_claims",
  {
    id: uuid("id").primaryKey().defaultRandom(),
    campaignId: varchar("campaign_id", { length: 64 })
      .notNull()
      .references(() => benefitCampaigns.id, { onDelete: "restrict" }),
    externalClaimId: uuid("external_claim_id").notNull().unique(),
    browserKeyHash: varchar("browser_key_hash", { length: 128 }).notNull(),
    networkKeyHash: varchar("network_key_hash", { length: 128 }).notNull(),
    status: varchar("status", { length: 16 }).notNull().default("pending"),
    opusUserId: text("opus_user_id"),
    opusDeviceId: text("opus_device_id"),
    subscriptionUrlEnc: text("subscription_url_enc"),
    expiresAt: timestamp("expires_at", { withTimezone: true }),
    attemptCount: integer("attempt_count").notNull().default(0),
    lastErrorCode: varchar("last_error_code", { length: 64 }),
    createdAt: timestamp("created_at", { withTimezone: true }).notNull().defaultNow(),
    updatedAt: timestamp("updated_at", { withTimezone: true }).notNull().defaultNow()
  },
  (table) => ({
    campaignBrowserUnique: unique("uniq_benefit_claims_campaign_browser").on(
      table.campaignId,
      table.browserKeyHash
    ),
    networkCreatedIdx: index("idx_benefit_claims_network_created").on(
      table.networkKeyHash,
      table.createdAt
    ),
    statusUpdatedIdx: index("idx_benefit_claims_status_updated").on(
      table.status,
      table.updatedAt
    ),
    statusCheck: check(
      "chk_benefit_claims_status",
      sql`${table.status} IN ('pending', 'provisioning', 'ready', 'failed', 'revoked', 'expired')`
    )
  })
);
