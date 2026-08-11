<img width="2290" height="687" alt="image" src="https://github.com/user-attachments/assets/39a7a9e3-aa5e-4abb-a52a-e5900a6470db" />

## ⚡ Quick Start

Install Heimdall with one command:

```bash
bash <(curl -Ls https://raw.githubusercontent.com/HeimdallStudio/Heimdall-Panel/main/install.sh)
```

During installation, Heimdall downloads the latest public release package, installs the panel, configures the system service, and walks you through the initial setup.

---

## ✨ What Makes Heimdall Different?

Heimdall is designed for operators who need more control, cleaner subscription delivery, and a more practical workflow for real-world Xray deployments.
It keeps the familiar panel experience, while adding operational tools for multi-profile subscriptions, per-client controls, infrastructure visibility, smarter routing, and easier service management.

The goal is to make daily operation cleaner, more flexible, and more reliable without making the panel unnecessarily complicated.


---

## 🧩 Multi-Profile Inbounds

Multi-Profile Inbounds allow a single inbound to serve multiple independent subscription profiles without duplicating the entire inbound configuration.

Each profile can define its own address, transport, security mode, display behavior, and subscription output. This makes it easier to manage multiple domains, brands, user groups, or routing strategies from one organized inbound structure.

The result is a cleaner backend, fewer duplicate entries, and a much more flexible subscription workflow.

<img width="1672" height="941" alt="multiProfile" src="https://github.com/user-attachments/assets/5e7bc87c-8ca9-4a08-b311-9c9a7af22d85" />


---

## 🚦 Per-Client Speed & Connection Limits

Per-client speed controls make it possible to define separate upload and download speed limits for each client, while also controlling how many concurrent connections the client is allowed to use.

Unlike a total traffic quota, which limits how much data a client can consume, speed limits control how fast each client can upload or download. This makes it easier to create service tiers, apply fair usage policies, and protect server capacity from heavy or abusive usage.

Concurrent connection limits add another layer of control by helping reduce account sharing and keeping resource usage more predictable across the server.

For deployments with many users, these controls make the service more stable, fair, and easier to operate.

<img width="1672" height="941" alt="file_00000000de9071f498c8a8e178843173" src="https://github.com/user-attachments/assets/0dbc02f7-155b-4225-8f55-86838c59a704" />

---

## 📊 Client Activity Monitoring

Client Activity Monitoring provides optional visibility into selected clients and their traffic behavior.

When enabled, it can help operators review observed destinations, traffic usage, and activity patterns. This is useful for abuse investigation, routing diagnostics, service quality checks, and understanding how traffic flows through the system.

The feature is designed for controlled operational use, without making the panel unnecessarily heavy or complicated.

<img width="1672" height="941" alt="ClientActivity" src="https://github.com/user-attachments/assets/cf19552e-51d8-4099-b2e4-23fc10b320d7" />

---

## 🕶️ Hidden Infrastructure

Hidden Infrastructure is managed through the y-ui terminal script and allows internal resources to be hidden without changing the actual runtime behavior.

Operators can hide inbound remarks, outbound tags, balancer tags, client emails, and routing-related entries from normal views or subscription outputs. This is useful for tunnel layers, internal routes, backend services, reseller structures, and operational-only configurations.

Hidden items continue to work normally in the background, while the visible panel and subscription output stay cleaner, safer, and easier to manage.

<img width="1672" height="941" alt="826ac078-92da-41f2-a483-993ce4e86edd" src="https://github.com/user-attachments/assets/6425e5af-ff34-4de6-9d5f-c425054e078f" />

---

## 🧭 Smart Subscription Links & Iran Direct Routing

Smart Subscription Links turn subscription output into a cleaner and more practical client experience, powered by the customized Ourenus-based subscription template.

Iran Direct Routing adds dedicated routing support for Iranian domains and IP ranges, allowing domestic traffic to be routed directly when direct routing is appropriate instead of passing through the proxy path.

This reduces unnecessary proxy load, improves access to local services, and creates smoother client profiles for users who frequently access Iranian websites, banking platforms, local applications, and domestic resources.

<img width="1672" height="941" alt="sub template" src="https://github.com/user-attachments/assets/626b2086-96cd-405f-9f6e-25ecb87357c8" />

## 🙏 Credits

Heimdall is built on top of the Xray ecosystem and is based on the excellent [3X-UI](https://github.com/MHSanaei/3x-ui/) project by MHSanaei.

It also integrates and customizes ideas from the [Ourenus](https://github.com/MatinDehghanian/Ourenus) subscription template, created by Matin Dehghanian, to provide a cleaner subscription experience.

Special thanks to the open-source projects, developers, and communities that make this ecosystem possible.



## 💛 Support the Project

Heimdall is developed and maintained with a focus on quality, stability, and real-world usability.

If you find this project useful and want to support its continued development, you can make a donation here:

[Donate to Heimdall](https://reymit.ir/heimdall)

Your support helps keep the project moving forward with more energy, better features, and long-term improvements.

