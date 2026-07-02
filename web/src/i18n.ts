import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import base from './locales/bundles/zh-CN'
import { BASE_LANG } from './locales'

// UI-copy i18n. The base language (简体中文) is bundled so first paint is synchronous;
// other languages are lazy-loaded and registered at runtime (see prefs.tsx / locales/).
// Report bodies are Chinese data and are not translated.
export const LOCALE_KEY = 'rp_locale'

i18n.use(initReactI18next).init({
  resources: { [BASE_LANG]: { translation: base.translation } },
  lng: BASE_LANG,
  fallbackLng: BASE_LANG,
  interpolation: { escapeValue: false },
})

export default i18n
