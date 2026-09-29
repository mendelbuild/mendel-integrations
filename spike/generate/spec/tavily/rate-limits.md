<!-- Fetched 2026-09-29 from docs.tavily.com -->
> ## Documentation Index
> Fetch the complete documentation index at: https://docs.tavily.com/llms.txt
> Use this file to discover all available pages before exploring further.

# Rate Limits

> Learn about Tavily's API rate limits for both  development and production environments.

We offer two types of rate limits based on the environment associated with your API key.

<Card icon="key" href="https://app.tavily.com" title="Get your API key" horizontal>
  Create your Development or Production API keys.
</Card>

| Environment | Requests per minute (RPM) |
| - | - |
| `Development` | 100 |
| `Production` | 1,000 |

## Crawl Endpoint Rate Limits

The crawl endpoint has a separate rate limit that applies to both development and production keys:

| Environment | Requests per minute (RPM) |
| - | - |
| `Development` | 100 |
| `Production` | 100 |

## Research Endpoint Rate Limits

The research endpoint has a separate rate limit that applies to both development and production keys for creating research tasks. Note that polling requests to retrieve the status of ongoing research tasks follow the default rate limits as decribed above.

| Environment | Requests per minute (RPM) |
| - | - |
| `Development` | 20 |
| `Production` | 20 |

## Usage Endpoint Rate Limits

The usage endpoint has a separate rate limit that applies to both development and production keys:

| Environment | Requests per 10 minutes |
| - | - |
| `Development` | 10 |
| `Production` | 10 |

## Handling Rate Limit Responses

When an API-key request exceeds a rate limit, the API returns `429 Too Many Requests`. If the response includes `Retry-After`, wait the indicated number of seconds before retrying. For example:

```http theme={null}
HTTP/2 429 Too Many Requests
Content-Type: application/json
Retry-After: 60

{
  "detail": {
    "error": "Your request has been blocked due to excessive requests. Please reduce the rate of requests."
  }
}
```

The 60-second delay is an example; use the value returned in the response. Error messages can vary by the limit reached; use the HTTP status and header for retry handling rather than matching the message text. [Keyless access](/documentation/keyless) uses a different error format and separate limits.

<Tip>
  1. Access to production keys requires either an active **Paid Plan** or **PAYGO** enabled. More information can be found [here](/guides/api-credits).
  2. When using the REST API, ensure you include your API key in the header to apply the correct rate limits.
</Tip>
