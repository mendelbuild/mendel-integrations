<!-- Fetched 2026-09-29 from docs.tavily.com -->
> ## Documentation Index
> Fetch the complete documentation index at: https://docs.tavily.com/llms.txt
> Use this file to discover all available pages before exploring further.

# Credits & Pricing

> Learn how to get and manage your Tavily API Credits.

## Free API Credits

<Card icon="key" href="https://app.tavily.com" title="Get your free API key" horizontal>
  You get 1,000 free API Credits every month. **No credit card required.**
</Card>

## Pricing Overview

Tavily operates on a simple, credit-based model:

* **Free**: 1,000 credits/month
* **Pay-as-you-go**: \$0.008 per credit (allows you to be charged per credit once your plan’s credit limit is reached).
* **Monthly plans**: \$0.0075 - \$0.005 per credit
* **Enterprise**: Custom pricing and volume

| **Plan** | **Credits per month** | **Monthly price** | **Price per credit** |
| - | - | - | - |
| **Researcher** | 1,000 | Free | - |
| **Project** | 4,000 | \$30 | \$0.0075 |
| **Bootstrap** | 15,000 | \$100 | \$0.0067 |
| **Startup** | 38,000 | \$220 | \$0.0058 |
| **Growth** | 100,000 | \$500 | \$0.005 |
| **Pay as you go** | Per usage | \$0.008 / Credit | \$0.008 |
| **Enterprise** | Custom | Custom | Custom |

Head to [billing](https://app.tavily.com/billing) to explore our different options and manage your plan.

## API Credits Costs

### Tavily Search

Your [search depth](/api-reference/endpoint/search#body-search-depth) determines the cost of your request.

* **Basic Search (`basic`):**
  Each request costs **1 API credit**.

* **Advanced Search (`advanced`):**
  Each request costs **2 API credits**.

### Tavily Extract

The number of successful URL extractions and your [extraction depth](/api-reference/endpoint/extract#body-extract-depth) determines the cost of your request. You never get charged if a URL extraction fails.

* **Basic Extract (`basic`):**
  Every 5 successful URL extractions cost **1 API credit**

* **Advanced Extract (`advanced`):**
  Every 5 successful URL extractions cost **2 API credits**

### Tavily Map

The number of pages mapped and whether or not natural-language [instructions](/documentation/api-reference/endpoint/map#instructions) are specified determines the cost of your request. You never get charged if a map request fails.

* **Regular Mapping:**
  Every 10 successful pages returned cost **1 API credit**

* **Map with (`instructions`):**
  Every 10 successful pages returned cost **2 API credits**

### Tavily Crawl

Tavily Crawl combines both mapping and extraction operations, so the cost is the sum of both:

* **Crawl Cost = Mapping Cost + Extraction Cost**

For example:

* If you crawl 10 pages with basic extraction depth, you'll be charged **1 credit for mapping** (10 pages) + **2 credits for extraction** (10 successful extractions ÷ 5) = **3 total credits**
* If you crawl 10 pages with advanced extraction depth, you'll be charged **1 credit for mapping** + **4 credits for extraction** = **5 total credits**

### Tavily Research

Tavily Research follows a dynamic
pricing model with minimum and maximum credit consumption boundaries associated
with each request. The minimum and maximum boundaries differ based on if the
request uses `model=mini` or `model=pro`.

| Request Cost Boundaries | model=pro | model=mini |
| - | - | - |
| Per-request minimum | 15 credits | 4 credits |
| Per-request maximum | 250 credits | 110 credits |
