# Heimdall Custom Subscription Templates

Heimdall can render subscription pages from custom HTML templates.

Heimdall releases include the customized Ourenus-based subscription template
used by the panel. You can also configure a separate custom template directory.

To use your own template:

1. Open the Heimdall panel settings.
2. Configure the custom subscription template option.
3. Provide the absolute path to the directory containing the template, for example
   `/etc/x-ui/sub_templates/my-theme/`.

Custom templates are independent from the bundled Ourenus-based template.

### Available Variables

When rendering the template, the following variables are injected into the template context (`{{ .variable }}`):

* `{{ .sId }}`: Subscription ID (UUID).
* `{{ .enabled }}`: Whether the subscription/client is enabled (boolean).
* `{{ .download }}`: Formatted download traffic (e.g. "2.5 GB").
* `{{ .upload }}`: Formatted upload traffic.
* `{{ .total }}`: Formatted total traffic limit.
* `{{ .used }}`: Formatted used traffic (download + upload).
* `{{ .remained }}`: Formatted remaining traffic.
* `{{ .expire }}`: Expiration time as an int64 Unix timestamp in **seconds** (`0` means never). Multiply by 1000 for a JavaScript `Date`.
* `{{ .lastOnline }}`: Last online time as an int64 Unix timestamp in **milliseconds** (`0` means never seen).
* `{{ .downloadByte }}`: Download traffic in exact bytes (int64).
* `{{ .uploadByte }}`: Upload traffic in exact bytes (int64).
* `{{ .totalByte }}`: Total traffic limit in exact bytes (int64).
* `{{ .subUrl }}`: The URL of the subscription page.
* `{{ .subJsonUrl }}`: The URL for the JSON configuration of the subscription.
* `{{ .subClashUrl }}`: The URL for the Clash/Mihomo configuration.
* `{{ .subTitle }}`: The subscription title configured in the panel (Subscription → Information). Useful for page branding/headings. May be empty.
* `{{ .subSupportUrl }}`: The support URL configured in the panel. Useful for a "Contact support" link. May be empty.
* `{{ .links }}`: A list (slice) of string configurations (VMess, VLESS, etc. URLs). You can loop through them using `{{ range .links }} ... {{ end }}`.
* `{{ .emails }}`: A list (slice) of emails related to the subscription.
* `{{ .datepicker }}`: Current calendar format used by the panel (e.g. "gregorian" or "jalali").
