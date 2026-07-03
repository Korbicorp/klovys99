"use strict";

const refreshIntervalMs = 5000;
const svgNamespace = "http://www.w3.org/2000/svg";
const colors = ["#076cd8", "#262626", "#16a34a", "#dc2626", "#9333ea", "#f59e0b", "#0f766e", "#64748b"];

const translations = {
  en: {
    documentTitle: "klovys99 Anonymization dashboard",
    proCta: "Explore klovys99 Pro",
    loading: "Loading",
    live: "Live",
    unavailable: "Unavailable",
    resetFailed: "Reset failed",
    refresh: "Refresh",
    resetStats: "Reset stats",
    dashboardTitle: "Anonymization dashboard",
    lastUpdated: "Last updated",
    never: "Never",
    protectionTitle: "Protection coverage",
    protectionsEnabled: (enabled, total) => `${enabled} / ${total} protections enabled`,
    enabledLabel: (enabled) => `${enabled} enabled`,
    availableLabel: (total) => `${total} available`,
    lowProtection: "Low protection",
    improvingProtection: "Improving",
    strongProtection: "Strong protection",
    optionsTitle: "Protection options",
    saveChanges: "Save changes",
    optionsSaved: "Saved",
    optionsUnsaved: "Unsaved changes",
    optionsSaving: "Saving",
    configUnavailable: "Config unavailable",
    optionEnabled: "Enabled",
    optionDisabled: "Disabled",
    enableAllOptions: "Enable all",
    disableAllOptions: "Disable all",
    enableCategory: "Enable category",
    disableCategory: "Disable category",
    categoryEnabledCount: (enabled, total) => `${enabled}/${total} enabled`,
    noOptionsAvailable: "No protection option available.",
    optionCategories: {
      identityContact: {
        label: "Identity & contact",
        description: "Personal identity and direct contact data.",
      },
      idsReferences: {
        label: "IDs & references",
        description: "Administrative, business, and contextual identifiers.",
      },
      secretsFinancial: {
        label: "Secrets & financial",
        description: "Credentials, tokens, network identifiers, and payment data.",
      },
      organizationsContext: {
        label: "Organizations & context",
        description: "Organizations, institutions, dates, and contextual personal signals.",
      },
      other: {
        label: "Other protections",
        description: "Types not assigned to a standard category yet.",
      },
    },
    exposureTitle: "Highest exposure signal",
    noExposureType: "None yet",
    replacements: (count) => `${count} ${count === "1" ? "replacement" : "replacements"}`,
    noSensitiveDetected: "No sensitive data type has been detected yet.",
    topExposureSuffix: "This is the most frequent data class klovys99 anonymized locally.",
    tableTitle: "Replacement types",
    typeHeader: "Type",
    countHeader: "Count",
    shareHeader: "Share",
    noTypeRecorded: "No replacement type recorded.",
    pieTitle: "Detected data by type",
    chartLegend: "Chart legend",
    detectedDistribution: "Detected data distribution",
    noAnonymizedData: "No anonymized data yet.",
    itemsUnit: "items",
    requestsUnit: "requests",
    timelineTitle: "Hourly activity",
    noActivity: "No activity recorded yet.",
    timelineAnonymized: "anonymized",
    timelineErrors: "errors",
    timelineRequests: "requests",
    healthTitle: "Operational errors",
    llmErrors: "LLM errors",
    proxyErrors: "Proxy errors",
    requestBodyErrors: "Request body errors",
    confirmReset: "Reset all local stats? This removes active and rotated stats files.",
    unknown: "Unknown",
    unknownTypeDescription: "Sensitive data detected by a local or external rule.",
    anonymizedRequests: "Anonymized requests",
    anonymizedRequestsDescription: "Requests where klovys99 replaced at least one sensitive value.",
    unchangedRequests: "Unchanged requests",
    unchangedRequestsDescription: "Requests processed without a detected sensitive replacement.",
  },
  fr: {
    documentTitle: "klovys99 Tableau de bord d'anonymisation",
    proCta: "Découvrir klovys99 Pro",
    loading: "Chargement",
    live: "En direct",
    unavailable: "Indisponible",
    resetFailed: "Échec du reset",
    refresh: "Actualiser",
    resetStats: "Réinitialiser",
    dashboardTitle: "Tableau de bord d'anonymisation",
    lastUpdated: "Dernière mise à jour",
    never: "Jamais",
    protectionTitle: "Couverture de protection",
    protectionsEnabled: (enabled, total) => `${enabled} / ${total} protections activées`,
    enabledLabel: (enabled) => `${enabled} activées`,
    availableLabel: (total) => `${total} disponibles`,
    lowProtection: "Protection faible",
    improvingProtection: "En progression",
    strongProtection: "Protection forte",
    optionsTitle: "Options de protection",
    saveChanges: "Enregistrer",
    optionsSaved: "Enregistré",
    optionsUnsaved: "Modifications non enregistrées",
    optionsSaving: "Enregistrement",
    configUnavailable: "Configuration indisponible",
    optionEnabled: "Activé",
    optionDisabled: "Désactivé",
    enableAllOptions: "Tout activer",
    disableAllOptions: "Tout désactiver",
    enableCategory: "Activer la catégorie",
    disableCategory: "Désactiver la catégorie",
    categoryEnabledCount: (enabled, total) => `${enabled}/${total} activées`,
    noOptionsAvailable: "Aucune option de protection disponible.",
    optionCategories: {
      identityContact: {
        label: "Identité & contact",
        description: "Données d'identité personnelle et de contact direct.",
      },
      idsReferences: {
        label: "IDs & références",
        description: "Identifiants administratifs, métier et contextuels.",
      },
      secretsFinancial: {
        label: "Secrets & financier",
        description: "Identifiants, tokens, données réseau et informations de paiement.",
      },
      organizationsContext: {
        label: "Organisations & contexte",
        description: "Organisations, institutions, dates et signaux personnels contextuels.",
      },
      other: {
        label: "Autres protections",
        description: "Types pas encore associés à une catégorie standard.",
      },
    },
    exposureTitle: "Signal d'exposition principal",
    noExposureType: "Aucun pour l'instant",
    replacements: (count) => `${count} ${count === "1" ? "remplacement" : "remplacements"}`,
    noSensitiveDetected: "Aucun type de donnée sensible n'a encore été détecté.",
    topExposureSuffix: "C'est la classe de données la plus souvent anonymisée localement par klovys99.",
    tableTitle: "Types de remplacements",
    typeHeader: "Type",
    countHeader: "Nombre",
    shareHeader: "Part",
    noTypeRecorded: "Aucun type de remplacement enregistré.",
    pieTitle: "Données détectées par type",
    chartLegend: "Légende du graphique",
    detectedDistribution: "Distribution des données détectées",
    noAnonymizedData: "Aucune donnée anonymisée pour l'instant.",
    itemsUnit: "éléments",
    requestsUnit: "requêtes",
    timelineTitle: "Activité horaire",
    noActivity: "Aucune activité enregistrée pour l'instant.",
    timelineAnonymized: "anonymisées",
    timelineErrors: "erreurs",
    timelineRequests: "requêtes",
    healthTitle: "Erreurs opérationnelles",
    llmErrors: "Erreurs LLM",
    proxyErrors: "Erreurs proxy",
    requestBodyErrors: "Erreurs de corps de requête",
    confirmReset: "Réinitialiser toutes les statistiques locales ? Cela supprime le fichier actif et les fichiers rotatés.",
    unknown: "Inconnu",
    unknownTypeDescription: "Donnée sensible détectée par une règle locale ou externe.",
    anonymizedRequests: "Requêtes anonymisées",
    anonymizedRequestsDescription: "Requêtes pour lesquelles klovys99 a remplacé au moins une valeur sensible.",
    unchangedRequests: "Requêtes inchangées",
    unchangedRequestsDescription: "Requêtes traitées sans remplacement sensible détecté.",
  },
};

const typeDescriptions = {
  en: {
    EMAIL: "Email addresses detected in prompts or context.",
    IP: "IP addresses, network endpoints, or infrastructure identifiers.",
    PHONE: "Phone numbers that can identify a person or organization contact.",
    NIR: "French social security identifiers.",
    FIRST_NAME: "First names detected as personal information.",
    LAST_NAME: "Last names detected as personal information.",
    ADDRESS: "Postal addresses or address-like location details.",
    IBAN: "Bank account identifiers.",
    CREDIT_CARD: "Payment card numbers.",
    MAC_ADDRESS: "Device network hardware identifiers.",
    CRYPTO: "Cryptographic keys, wallets, tokens, or similar sensitive values.",
    SECRET: "API keys, passwords, tokens, credentials, or secret-like values.",
    GENERIC_ID: "Generic internal identifiers, account IDs, UUIDs, or labeled IDs.",
    NUMERIC_ID: "Numeric identifiers that may refer to users, tickets, orders, or records.",
    REFERENCE_ID: "Business references, case IDs, tickets, invoices, or tracking IDs.",
    NAME: "Names detected when first and last name boundaries are ambiguous.",
    PERSON_NAME: "Full person names.",
    LOCATION: "Locations, cities, countries, or place names.",
    ORGANIZATION: "Company, institution, or organization names.",
    CONTEXT_IDENTIFIER: "Context-specific identifiers extracted from nearby labels.",
    OTHER_PII: "Other personal information detected by external recognizers.",
    DATE: "Dates that may reveal personal, contractual, or operational context.",
    BLOOD_TYPE: "Medical blood type information.",
    DOCUMENT_ID: "Document, passport, identity, or administrative identifiers.",
    VEHICLE_PLATE: "Vehicle registration identifiers.",
    MEDICAL_PROVIDER: "Healthcare provider names or medical institution references.",
    SCHOOL: "School or education institution names.",
    EMPLOYER: "Employer or workplace identifiers.",
    PET_IDENTIFIER: "Pet names or pet-related identifiers that can reveal personal context.",
  },
  fr: {
    EMAIL: "Adresses e-mail détectées dans les prompts ou le contexte.",
    IP: "Adresses IP, endpoints réseau ou identifiants d'infrastructure.",
    PHONE: "Numéros de téléphone pouvant identifier une personne ou un contact d'organisation.",
    NIR: "Identifiants français de sécurité sociale.",
    FIRST_NAME: "Prénoms détectés comme données personnelles.",
    LAST_NAME: "Noms de famille détectés comme données personnelles.",
    ADDRESS: "Adresses postales ou informations de localisation similaires.",
    IBAN: "Identifiants de compte bancaire.",
    CREDIT_CARD: "Numéros de carte de paiement.",
    MAC_ADDRESS: "Identifiants matériels réseau d'un appareil.",
    CRYPTO: "Clés cryptographiques, wallets, tokens ou valeurs sensibles similaires.",
    SECRET: "Clés API, mots de passe, tokens, identifiants ou valeurs ressemblant à des secrets.",
    GENERIC_ID: "Identifiants internes génériques, comptes, UUID ou IDs étiquetés.",
    NUMERIC_ID: "Identifiants numériques pouvant référencer des utilisateurs, tickets, commandes ou dossiers.",
    REFERENCE_ID: "Références métier, dossiers, tickets, factures ou IDs de suivi.",
    NAME: "Noms détectés quand les limites prénom/nom sont ambiguës.",
    PERSON_NAME: "Noms complets de personnes.",
    LOCATION: "Lieux, villes, pays ou noms d'emplacements.",
    ORGANIZATION: "Noms d'entreprises, institutions ou organisations.",
    CONTEXT_IDENTIFIER: "Identifiants propres au contexte extraits autour de libellés.",
    OTHER_PII: "Autres données personnelles détectées par des recognizers externes.",
    DATE: "Dates pouvant révéler un contexte personnel, contractuel ou opérationnel.",
    BLOOD_TYPE: "Informations médicales de groupe sanguin.",
    DOCUMENT_ID: "Identifiants de documents, passeports, identités ou documents administratifs.",
    VEHICLE_PLATE: "Identifiants d'immatriculation de véhicule.",
    MEDICAL_PROVIDER: "Noms de professionnels ou établissements de santé.",
    SCHOOL: "Noms d'écoles ou d'établissements d'enseignement.",
    EMPLOYER: "Identifiants d'employeurs ou de lieux de travail.",
    PET_IDENTIFIER: "Noms ou identifiants d'animaux pouvant révéler un contexte personnel.",
  },
};

const language = detectLanguage();
const locale = language === "fr" ? "fr-FR" : "en-US";
const text = translations[language];
const numberFormat = new Intl.NumberFormat(locale);
const percentFormat = new Intl.NumberFormat(locale, { maximumFractionDigits: 1 });
const timeFormat = new Intl.DateTimeFormat(locale, {
  month: "short",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
});
const defaultProtectionOptions = Object.keys(typeDescriptions.en).map((type) => ({
  type,
  enabled: true,
}));
const categoryDefinitions = [
  {
    id: "identityContact",
    types: ["FIRST_NAME", "LAST_NAME", "NAME", "PERSON_NAME", "EMAIL", "PHONE", "ADDRESS", "LOCATION"],
  },
  {
    id: "idsReferences",
    types: ["GENERIC_ID", "NUMERIC_ID", "REFERENCE_ID", "DOCUMENT_ID", "NIR", "VEHICLE_PLATE", "CONTEXT_IDENTIFIER"],
  },
  {
    id: "secretsFinancial",
    types: ["SECRET", "CRYPTO", "IBAN", "CREDIT_CARD", "IP", "MAC_ADDRESS"],
  },
  {
    id: "organizationsContext",
    types: ["ORGANIZATION", "EMPLOYER", "SCHOOL", "MEDICAL_PROVIDER", "DATE", "OTHER_PII", "BLOOD_TYPE", "PET_IDENTIFIER"],
  },
];

const elements = {
  status: document.querySelector("#connectionStatus"),
  refreshButton: document.querySelector("#refreshButton"),
  resetButton: document.querySelector("#resetButton"),
  proCtaLabel: document.querySelector("#proCtaLabel"),
  lastUpdatedLabel: document.querySelector("#lastUpdatedLabel"),
  lastUpdated: document.querySelector("#lastUpdated"),
  dashboardTitle: document.querySelector("#dashboardTitle"),
  protectionPanel: document.querySelector(".protection-panel"),
  protectionTitle: document.querySelector("#protectionTitle"),
  protectionRate: document.querySelector("#protectionRate"),
  protectedRatio: document.querySelector("#protectedRatio"),
  protectionBarFill: document.querySelector("#protectionBarFill"),
  protectedBarLabel: document.querySelector("#protectedBarLabel"),
  totalBarLabel: document.querySelector("#totalBarLabel"),
  lowProtectionLabel: document.querySelector("#lowProtectionLabel"),
  improvingProtectionLabel: document.querySelector("#improvingProtectionLabel"),
  strongProtectionLabel: document.querySelector("#strongProtectionLabel"),
  optionsTitle: document.querySelector("#optionsTitle"),
  optionsStatus: document.querySelector("#optionsStatus"),
  enableAllOptionsButton: document.querySelector("#enableAllOptionsButton"),
  disableAllOptionsButton: document.querySelector("#disableAllOptionsButton"),
  saveOptionsButton: document.querySelector("#saveOptionsButton"),
  protectionOptionsList: document.querySelector("#protectionOptionsList"),
  optionsEmpty: document.querySelector("#optionsEmpty"),
  exposureTitle: document.querySelector("#exposureTitle"),
  topExposureType: document.querySelector("#topExposureType"),
  topExposureCount: document.querySelector("#topExposureCount"),
  topExposureDescription: document.querySelector("#topExposureDescription"),
  tableTitle: document.querySelector("#tableTitle"),
  typeHeader: document.querySelector("#typeHeader"),
  countHeader: document.querySelector("#countHeader"),
  shareHeader: document.querySelector("#shareHeader"),
  typeRows: document.querySelector("#typeRows"),
  typeEmpty: document.querySelector("#typeEmpty"),
  pieTitle: document.querySelector("#pieTitle"),
  pieChart: document.querySelector("#pieChart"),
  pieTotal: document.querySelector("#pieTotal"),
  pieUnit: document.querySelector("#pieUnit"),
  pieLegend: document.querySelector("#pieLegend"),
  pieEmpty: document.querySelector("#pieEmpty"),
  timelineTitle: document.querySelector("#timelineTitle"),
  timelineRows: document.querySelector("#timelineRows"),
  timelineEmpty: document.querySelector("#timelineEmpty"),
  healthTitle: document.querySelector("#healthTitle"),
  llmErrorsLabel: document.querySelector("#llmErrorsLabel"),
  proxyErrorsLabel: document.querySelector("#proxyErrorsLabel"),
  requestBodyErrorsLabel: document.querySelector("#requestBodyErrorsLabel"),
  llmErrors: document.querySelector("#llmErrors"),
  proxyErrors: document.querySelector("#proxyErrors"),
  requestBodyErrors: document.querySelector("#requestBodyErrors"),
};

let statsLoading = false;
let configLoading = false;
let configSaving = false;
let configUnavailable = false;
let savedProtectionOptions = cloneProtectionOptions(defaultProtectionOptions);
let draftProtectionOptions = cloneProtectionOptions(defaultProtectionOptions);

renderStaticText();
renderProtectionOptions();
renderProtectionCoverage();

elements.refreshButton.addEventListener("click", () => {
  void loadDashboard();
});

elements.resetButton.addEventListener("click", () => {
  void resetStats();
});

elements.enableAllOptionsButton.addEventListener("click", () => {
  updateAllDraftOptions(true);
});

elements.disableAllOptionsButton.addEventListener("click", () => {
  updateAllDraftOptions(false);
});

elements.saveOptionsButton.addEventListener("click", () => {
  void saveProtectionOptions();
});

void loadDashboard();
window.setInterval(() => {
  void loadStats();
}, refreshIntervalMs);

// loadDashboard refreshes stats and the saved app config as separate backend resources.
async function loadDashboard() {
  await Promise.all([loadStats(), loadConfig()]);
}

async function loadStats() {
  if (statsLoading) {
    return;
  }
  statsLoading = true;
  setStatus("loading", text.loading);
  try {
    const response = await fetch("/api/stats", { cache: "no-store" });
    if (!response.ok) {
      throw new Error(`Stats API returned ${response.status}`);
    }
    const summary = normalizeSummary(await response.json());
    renderSummary(summary);
    setStatus("live", text.live);
    elements.lastUpdated.textContent = new Date().toLocaleTimeString(locale, {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  } catch (error) {
    console.error(error);
    setStatus("error", text.unavailable);
  } finally {
    statsLoading = false;
  }
}

// loadConfig reads the persisted protection toggles without overwriting unsaved edits.
async function loadConfig() {
  if (configLoading || configSaving || hasOptionChanges()) {
    updateOptionsState();
    return;
  }
  configLoading = true;
  configUnavailable = false;
  updateOptionsState();
  try {
    const response = await fetch("/api/config", { cache: "no-store" });
    if (!response.ok) {
      throw new Error(`Config API returned ${response.status}`);
    }
    const config = normalizeConfig(await response.json());
    savedProtectionOptions = cloneProtectionOptions(config.protection_options);
    draftProtectionOptions = cloneProtectionOptions(config.protection_options);
  } catch (error) {
    console.error(error);
    configUnavailable = true;
  } finally {
    configLoading = false;
    renderProtectionOptions();
    renderProtectionCoverage();
  }
}

// saveProtectionOptions persists the current toggle draft to the global app config.
async function saveProtectionOptions() {
  if (configSaving || !hasOptionChanges()) {
    return;
  }
  configSaving = true;
  configUnavailable = false;
  updateOptionsState();
  try {
    const response = await fetch("/api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ protection_options: draftProtectionOptions }),
    });
    if (!response.ok) {
      throw new Error(`Config update returned ${response.status}`);
    }
    const config = normalizeConfig(await response.json());
    savedProtectionOptions = cloneProtectionOptions(config.protection_options);
    draftProtectionOptions = cloneProtectionOptions(config.protection_options);
  } catch (error) {
    console.error(error);
    configUnavailable = true;
  } finally {
    configSaving = false;
    renderProtectionOptions();
    renderProtectionCoverage();
  }
}

async function resetStats() {
  const confirmed = window.confirm(text.confirmReset);
  if (!confirmed) {
    return;
  }

  elements.resetButton.disabled = true;
  try {
    const response = await fetch("/api/stats/reset", { method: "POST" });
    if (!response.ok) {
      throw new Error(`Stats reset returned ${response.status}`);
    }
    await loadStats();
  } catch (error) {
    console.error(error);
    setStatus("error", text.resetFailed);
  } finally {
    elements.resetButton.disabled = false;
  }
}

function renderStaticText() {
  document.documentElement.lang = language;
  document.title = text.documentTitle;
  elements.proCtaLabel.textContent = text.proCta;
  elements.refreshButton.textContent = text.refresh;
  elements.resetButton.textContent = text.resetStats;
  elements.dashboardTitle.textContent = text.dashboardTitle;
  elements.lastUpdatedLabel.textContent = text.lastUpdated;
  elements.lastUpdated.textContent = text.never;
  setHeading(elements.protectionTitle, "🛡️", text.protectionTitle);
  setHeading(elements.optionsTitle, "⚙️", text.optionsTitle);
  setHeading(elements.exposureTitle, "⚠️", text.exposureTitle);
  setHeading(elements.tableTitle, "🏷️", text.tableTitle);
  setHeading(elements.pieTitle, "📊", text.pieTitle);
  setHeading(elements.timelineTitle, "⏱️", text.timelineTitle);
  setHeading(elements.healthTitle, "🧭", text.healthTitle);
  elements.lowProtectionLabel.textContent = text.lowProtection;
  elements.improvingProtectionLabel.textContent = text.improvingProtection;
  elements.strongProtectionLabel.textContent = text.strongProtection;
  elements.enableAllOptionsButton.textContent = text.enableAllOptions;
  elements.disableAllOptionsButton.textContent = text.disableAllOptions;
  elements.saveOptionsButton.textContent = text.saveChanges;
  elements.optionsEmpty.textContent = text.noOptionsAvailable;
  elements.typeHeader.textContent = text.typeHeader;
  elements.countHeader.textContent = text.countHeader;
  elements.shareHeader.textContent = text.shareHeader;
  elements.typeEmpty.textContent = text.noTypeRecorded;
  elements.pieEmpty.textContent = text.noAnonymizedData;
  elements.pieLegend.setAttribute("aria-label", text.chartLegend);
  elements.pieChart.setAttribute("aria-label", text.detectedDistribution);
  elements.timelineEmpty.textContent = text.noActivity;
  elements.llmErrorsLabel.textContent = text.llmErrors;
  elements.proxyErrorsLabel.textContent = text.proxyErrors;
  elements.requestBodyErrorsLabel.textContent = text.requestBodyErrors;
}

function setHeading(element, emoji, label) {
  element.innerHTML = `<span aria-hidden="true">${emoji}</span>${escapeHtml(label)}`;
}

function setStatus(status, label) {
  elements.status.dataset.status = status;
  elements.status.textContent = label;
}

function normalizeSummary(summary) {
  return {
    total_requests: safeNumber(summary.total_requests),
    anonymized_requests: safeNumber(summary.anonymized_requests),
    llm_errors: safeNumber(summary.llm_errors),
    proxy_errors: safeNumber(summary.proxy_errors),
    request_body_errors: safeNumber(summary.request_body_errors),
    total_replacements: safeNumber(summary.total_replacements),
    counts_by_type: Array.isArray(summary.counts_by_type) ? summary.counts_by_type : [],
    timeline: Array.isArray(summary.timeline) ? summary.timeline : [],
  };
}

function renderSummary(summary) {
  renderProtectionCoverage();
  renderTopExposure(summary);
  renderPie(summary);
  renderTypeTable(summary);
  renderTimeline(summary.timeline);
  renderHealth(summary);
}

function renderProtectionCoverage() {
  const options = draftProtectionOptions;
  const total = options.length;
  const enabled = options.filter((option) => option.enabled).length;
  const rate = total > 0 ? (enabled / total) * 100 : 0;
  const boundedRate = Math.min(Math.max(rate, 0), 100);
  const protectionColor = protectionColorForRate(boundedRate);

  elements.protectionRate.textContent = `${percentFormat.format(rate)}%`;
  elements.protectedRatio.textContent = text.protectionsEnabled(formatNumber(enabled), formatNumber(total));
  elements.protectionBarFill.style.width = `${boundedRate}%`;
  elements.protectionPanel.style.setProperty("--protection-color", protectionColor);
  elements.protectedBarLabel.textContent = text.enabledLabel(formatNumber(enabled));
  elements.totalBarLabel.textContent = text.availableLabel(formatNumber(total));
}

function protectionColorForRate(rate) {
  if (rate >= 80) {
    return "#16a34a";
  }
  if (rate >= 40) {
    return "#f97316";
  }
  return "#facc15";
}

// normalizeConfig adapts the backend config payload to the dashboard view model.
function normalizeConfig(config) {
  const payload = config || {};
  return {
    protection_options: normalizeProtectionOptions(payload.protection_options || payload.protectionOptions),
  };
}

// normalizeProtectionOptions keeps only usable toggle entries and falls back to defaults.
function normalizeProtectionOptions(rawOptions) {
  if (Array.isArray(rawOptions)) {
    const options = rawOptions
      .map((option) => {
        const type = String(option.type || option.name || option.id || "").trim();
        if (type === "") {
          return null;
        }
        return {
          type,
          enabled: Boolean(option.enabled ?? option.active ?? option.is_enabled),
        };
      })
      .filter(Boolean);
    return options.length > 0 ? options : cloneProtectionOptions(defaultProtectionOptions);
  }

  return cloneProtectionOptions(defaultProtectionOptions);
}

// renderProtectionOptions draws protection toggles grouped into user-facing categories.
function renderProtectionOptions() {
  const categories = groupProtectionOptions(draftProtectionOptions);
  elements.protectionOptionsList.innerHTML = "";
  elements.optionsEmpty.hidden = draftProtectionOptions.length > 0;

  categories.forEach((category) => {
    elements.protectionOptionsList.appendChild(renderProtectionCategory(category));
  });

  updateOptionsState();
}

// renderProtectionCategory draws one category card with bulk controls and child toggles.
function renderProtectionCategory(category) {
  const metadata = text.optionCategories[category.id] || text.optionCategories.other;
  const enabledCount = category.options.filter((option) => option.enabled).length;
  const categoryElement = document.createElement("section");
  categoryElement.className = "protection-category";
  categoryElement.innerHTML = `
    <div class="category-header">
      <div>
        <div class="category-title">
          <strong>${escapeHtml(metadata.label)}</strong>
          <span class="category-count">${escapeHtml(text.categoryEnabledCount(formatNumber(enabledCount), formatNumber(category.options.length)))}</span>
        </div>
        <p class="category-description">${escapeHtml(metadata.description)}</p>
      </div>
      <div class="category-actions">
        <button class="button button-secondary button-small" type="button" data-action="enable">
          ${escapeHtml(text.enableCategory)}
        </button>
        <button class="button button-secondary button-small" type="button" data-action="disable">
          ${escapeHtml(text.disableCategory)}
        </button>
      </div>
    </div>
    <div class="category-options"></div>
  `;

  const enableButton = categoryElement.querySelector('[data-action="enable"]');
  const disableButton = categoryElement.querySelector('[data-action="disable"]');
  enableButton.disabled = enabledCount === category.options.length;
  disableButton.disabled = enabledCount === 0;
  enableButton.addEventListener("click", () => {
    updateDraftOptionsForTypes(
      category.options.map((option) => option.type),
      true,
    );
  });
  disableButton.addEventListener("click", () => {
    updateDraftOptionsForTypes(
      category.options.map((option) => option.type),
      false,
    );
  });

  const optionsContainer = categoryElement.querySelector(".category-options");
  category.options.forEach((option) => {
    optionsContainer.appendChild(renderProtectionOption(option));
  });
  return categoryElement;
}

// renderProtectionOption draws one anonymization type toggle.
function renderProtectionOption(option) {
  const optionID = `protection-option-${option.type}`;
  const item = document.createElement("div");
  item.className = "protection-option";
  item.title = `${option.type}: ${descriptionForType(option.type)}`;
  item.innerHTML = `
    <div class="option-copy">
      <strong>${escapeHtml(option.type)}</strong>
      <span>${escapeHtml(descriptionForType(option.type))}</span>
    </div>
    <label class="option-switch" for="${escapeHtml(optionID)}">
      <input id="${escapeHtml(optionID)}" type="checkbox" ${option.enabled ? "checked" : ""} />
      <span class="option-switch-control" aria-hidden="true"></span>
      <span class="option-switch-text">${escapeHtml(option.enabled ? text.optionEnabled : text.optionDisabled)}</span>
    </label>
  `;

  const input = item.querySelector("input");
  input.addEventListener("change", () => {
    updateDraftOption(option.type, input.checked);
  });
  return item;
}

// groupProtectionOptions assigns the flat backend config to dashboard-only categories.
function groupProtectionOptions(options) {
  const optionsByType = new Map(options.map((option) => [option.type, option]));
  const usedTypes = new Set();
  const categories = categoryDefinitions
    .map((definition) => {
      const categoryOptions = definition.types
        .map((type) => {
          const option = optionsByType.get(type);
          if (option) {
            usedTypes.add(type);
          }
          return option;
        })
        .filter(Boolean);
      return { id: definition.id, options: categoryOptions };
    })
    .filter((category) => category.options.length > 0);

  const remainingOptions = options.filter((option) => !usedTypes.has(option.type));
  if (remainingOptions.length > 0) {
    categories.push({ id: "other", options: remainingOptions });
  }
  return categories;
}

// updateDraftOption changes the browser draft without saving it to the backend yet.
function updateDraftOption(type, enabled) {
  updateDraftOptionsForTypes([type], enabled);
}

// updateAllDraftOptions enables or disables every protection option in the browser draft.
function updateAllDraftOptions(enabled) {
  updateDraftOptionsForTypes(
    draftProtectionOptions.map((option) => option.type),
    enabled,
  );
}

// updateDraftOptionsForTypes changes selected types without saving them to the backend yet.
function updateDraftOptionsForTypes(types, enabled) {
  const selectedTypes = new Set(types);
  draftProtectionOptions = draftProtectionOptions.map((option) => (selectedTypes.has(option.type) ? { ...option, enabled } : option));
  renderProtectionOptions();
  renderProtectionCoverage();
}

// updateOptionsState keeps the save button and status pill aligned with config state.
function updateOptionsState() {
  const dirty = hasOptionChanges();
  const enabledCount = draftProtectionOptions.filter((option) => option.enabled).length;
  const busy = configLoading || configSaving;
  elements.enableAllOptionsButton.disabled = busy || enabledCount === draftProtectionOptions.length;
  elements.disableAllOptionsButton.disabled = busy || enabledCount === 0;
  elements.saveOptionsButton.disabled = configLoading || configSaving || !dirty;
  if (configLoading) {
    elements.optionsStatus.dataset.status = "loading";
    elements.optionsStatus.textContent = text.loading;
    return;
  }
  if (configSaving) {
    elements.optionsStatus.dataset.status = "saving";
    elements.optionsStatus.textContent = text.optionsSaving;
    return;
  }
  if (configUnavailable) {
    elements.optionsStatus.dataset.status = "error";
    elements.optionsStatus.textContent = text.configUnavailable;
    return;
  }
  if (dirty) {
    elements.optionsStatus.dataset.status = "dirty";
    elements.optionsStatus.textContent = text.optionsUnsaved;
    return;
  }
  elements.optionsStatus.dataset.status = "saved";
  elements.optionsStatus.textContent = text.optionsSaved;
}

// hasOptionChanges compares the saved backend config with the browser draft.
function hasOptionChanges() {
  if (savedProtectionOptions.length !== draftProtectionOptions.length) {
    return true;
  }
  const savedByType = new Map(savedProtectionOptions.map((option) => [option.type, option.enabled]));
  return draftProtectionOptions.some((option) => savedByType.get(option.type) !== option.enabled);
}

// cloneProtectionOptions copies toggle state so saved and draft objects never alias.
function cloneProtectionOptions(options) {
  return options.map((option) => ({
    type: String(option.type),
    enabled: Boolean(option.enabled),
  }));
}

function renderTopExposure(summary) {
  const topType = normalizedTypeCounts(summary)[0];
  if (!topType) {
    elements.topExposureType.textContent = text.noExposureType;
    elements.topExposureCount.textContent = text.replacements("0");
    elements.topExposureDescription.textContent = text.noSensitiveDetected;
    return;
  }

  elements.topExposureType.textContent = topType.type;
  elements.topExposureCount.textContent = text.replacements(formatNumber(topType.count));
  elements.topExposureDescription.textContent = `${descriptionForType(topType.type)} ${text.topExposureSuffix}`;
}

function renderHealth(summary) {
  renderHealthValue(elements.llmErrors, summary.llm_errors);
  renderHealthValue(elements.proxyErrors, summary.proxy_errors);
  renderHealthValue(elements.requestBodyErrors, summary.request_body_errors);
}

function renderHealthValue(element, value) {
  element.textContent = formatNumber(value);
  element.dataset.hasErrors = value > 0 ? "true" : "false";
}

function renderPie(summary) {
  const typeSlices = normalizedTypeCounts(summary).map((item) => ({
    label: item.type,
    value: item.count,
    description: descriptionForType(item.type),
  }));

  const slices = typeSlices.length > 0 ? typeSlices : requestFallbackSlices(summary);
  const total = slices.reduce((sum, item) => sum + item.value, 0);

  elements.pieEmpty.hidden = total > 0;
  elements.pieTotal.textContent = formatNumber(total);
  elements.pieUnit.textContent = typeSlices.length > 0 ? text.itemsUnit : text.requestsUnit;
  elements.pieLegend.innerHTML = "";
  renderPieSvg(slices, total);

  slices.forEach((slice, index) => {
    const item = document.createElement("li");
    item.className = "legend-item";
    item.title = `${slice.label}: ${slice.description}`;
    item.innerHTML = `
      <span class="legend-color" style="background:${colors[index % colors.length]}"></span>
      <span class="legend-label">${escapeHtml(slice.label)}</span>
      <span class="legend-value">${formatNumber(slice.value)}</span>
    `;
    elements.pieLegend.appendChild(item);
  });
}

function renderPieSvg(slices, total) {
  elements.pieChart.replaceChildren();
  if (total <= 0) {
    const circle = createSvgElement("circle", {
      class: "pie-empty-ring",
      cx: "120",
      cy: "120",
      r: "112",
    });
    elements.pieChart.appendChild(circle);
    return;
  }

  let current = 0;
  slices.forEach((slice, index) => {
    const startAngle = -90 + current * 360;
    const sliceShare = slice.value / total;
    const endAngle = -90 + (current + sliceShare) * 360;
    current += sliceShare;

    const path = createSvgElement("path", {
      class: "pie-slice",
      d: describeDonutSegment(120, 120, 112, 58, startAngle, endAngle),
      fill: colors[index % colors.length],
      tabindex: "0",
    });
    const title = createSvgElement("title");
    title.textContent = `${slice.label}: ${formatNumber(slice.value)} (${percentFormat.format(sliceShare * 100)}%). ${slice.description}`;
    path.appendChild(title);
    elements.pieChart.appendChild(path);
  });
}

function requestFallbackSlices(summary) {
  if (summary.total_requests <= 0) {
    return [];
  }
  const unchanged = Math.max(summary.total_requests - summary.anonymized_requests, 0);
  return [
    {
      label: text.anonymizedRequests,
      value: summary.anonymized_requests,
      description: text.anonymizedRequestsDescription,
    },
    {
      label: text.unchangedRequests,
      value: unchanged,
      description: text.unchangedRequestsDescription,
    },
  ].filter((item) => item.value > 0);
}

function renderTypeTable(summary) {
  const rows = normalizedTypeCounts(summary);
  const total = rows.reduce((sum, item) => sum + item.count, 0);

  elements.typeRows.innerHTML = "";
  elements.typeEmpty.hidden = rows.length > 0;

  rows.forEach((row) => {
    const tr = document.createElement("tr");
    const share = total > 0 ? `${percentFormat.format((row.count / total) * 100)}%` : "0%";
    const description = descriptionForType(row.type);
    tr.title = `${row.type}: ${description}`;
    tr.innerHTML = `
      <td>
        <span class="type-name">${escapeHtml(row.type)}</span>
        <span class="type-description">${escapeHtml(description)}</span>
      </td>
      <td>${formatNumber(row.count)}</td>
      <td>${share}</td>
    `;
    elements.typeRows.appendChild(tr);
  });
}

function renderTimeline(timeline) {
  const buckets = timeline
    .map((bucket) => ({
      bucket: bucket.bucket,
      requests: safeNumber(bucket.requests),
      anonymized: safeNumber(bucket.anonymized_requests),
      replacements: safeNumber(bucket.total_replacements),
      errors: safeNumber(bucket.llm_errors) + safeNumber(bucket.proxy_errors) + safeNumber(bucket.request_body_errors),
    }))
    .filter((bucket) => bucket.requests > 0 || bucket.replacements > 0 || bucket.errors > 0)
    .slice(-12);

  const maxRequests = Math.max(...buckets.map((bucket) => bucket.requests), 1);
  elements.timelineRows.innerHTML = "";
  elements.timelineEmpty.hidden = buckets.length > 0;

  buckets.forEach((bucket) => {
    const row = document.createElement("div");
    row.className = "timeline-row";
    const width = Math.max((bucket.requests / maxRequests) * 100, bucket.requests > 0 ? 8 : 0);
    row.innerHTML = `
      <div class="timeline-time">${escapeHtml(formatBucket(bucket.bucket))}</div>
      <div class="timeline-bar-track">
        <div class="timeline-bar" style="width:${width}%"></div>
        <div class="timeline-details">
          <span>${formatNumber(bucket.anonymized)} ${escapeHtml(text.timelineAnonymized)}</span>
          <span class="timeline-errors">${formatNumber(bucket.errors)} ${escapeHtml(text.timelineErrors)}</span>
        </div>
      </div>
      <div class="timeline-count">${formatNumber(bucket.requests)} ${escapeHtml(text.timelineRequests)}</div>
    `;
    elements.timelineRows.appendChild(row);
  });
}

function normalizedTypeCounts(summary) {
  return summary.counts_by_type
    .map((item) => ({ type: String(item.type || text.unknown), count: safeNumber(item.count) }))
    .filter((item) => item.count > 0);
}

function descriptionForType(type) {
  return typeDescriptions[language][type] || typeDescriptions.en[type] || text.unknownTypeDescription;
}

function describeDonutSegment(cx, cy, outerRadius, innerRadius, startAngle, endAngle) {
  const fullCircleSafeEnd = endAngle - startAngle >= 360 ? startAngle + 359.999 : endAngle;
  const startOuter = polarToCartesian(cx, cy, outerRadius, startAngle);
  const endOuter = polarToCartesian(cx, cy, outerRadius, fullCircleSafeEnd);
  const startInner = polarToCartesian(cx, cy, innerRadius, fullCircleSafeEnd);
  const endInner = polarToCartesian(cx, cy, innerRadius, startAngle);
  const largeArcFlag = fullCircleSafeEnd - startAngle > 180 ? "1" : "0";

  return [
    `M ${startOuter.x} ${startOuter.y}`,
    `A ${outerRadius} ${outerRadius} 0 ${largeArcFlag} 1 ${endOuter.x} ${endOuter.y}`,
    `L ${startInner.x} ${startInner.y}`,
    `A ${innerRadius} ${innerRadius} 0 ${largeArcFlag} 0 ${endInner.x} ${endInner.y}`,
    "Z",
  ].join(" ");
}

function polarToCartesian(cx, cy, radius, angleInDegrees) {
  const angleInRadians = (angleInDegrees * Math.PI) / 180;
  return {
    x: cx + radius * Math.cos(angleInRadians),
    y: cy + radius * Math.sin(angleInRadians),
  };
}

function createSvgElement(name, attributes = {}) {
  const element = document.createElementNS(svgNamespace, name);
  Object.entries(attributes).forEach(([key, value]) => {
    element.setAttribute(key, value);
  });
  return element;
}

function formatBucket(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return text.unknown;
  }
  return timeFormat.format(date);
}

function safeNumber(value) {
  return Number.isFinite(Number(value)) ? Number(value) : 0;
}

function formatNumber(value) {
  return numberFormat.format(safeNumber(value));
}

function detectLanguage() {
  const languages = navigator.languages && navigator.languages.length > 0 ? navigator.languages : [navigator.language || "en"];
  const primaryLanguage = String(languages[0] || "en").toLowerCase();
  return primaryLanguage.startsWith("fr") ? "fr" : "en";
}

function escapeHtml(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}
