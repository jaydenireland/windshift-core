import { i18n } from '../stores/i18n.svelte.js';

// History-diff UI strings, kept here instead of the app's locale catalogs so
// the fork never edits upstream locale files (same approach as
// `collab/strings.js`). Every application locale is covered; unknown locales
// fall back to English.
const STRINGS = {
  en: {
    unchanged: (n) => `${n} unchanged ${n === 1 ? 'word' : 'words'}`,
    showLess: 'Show less',
    added: 'Added',
    removed: 'Removed',
  },
  de: {
    unchanged: (n) => `${n} unveränderte ${n === 1 ? 'Wort' : 'Wörter'}`,
    showLess: 'Weniger anzeigen',
    added: 'Hinzugefügt',
    removed: 'Entfernt',
  },
  fr: {
    unchanged: (n) => `${n} ${n === 1 ? 'mot inchangé' : 'mots inchangés'}`,
    showLess: 'Afficher moins',
    added: 'Ajouté',
    removed: 'Supprimé',
  },
  ru: {
    unchanged: (n) => `${n} без изменений`,
    showLess: 'Свернуть',
    added: 'Добавлено',
    removed: 'Удалено',
  },
  es: {
    unchanged: (n) => `${n} ${n === 1 ? 'palabra sin cambios' : 'palabras sin cambios'}`,
    showLess: 'Mostrar menos',
    added: 'Añadido',
    removed: 'Eliminado',
  },
  ar: {
    unchanged: (n) => `${n} كلمات بدون تغيير`,
    showLess: 'عرض أقل',
    added: 'مُضاف',
    removed: 'محذوف',
  },
  ko: {
    unchanged: (n) => `변경되지 않은 단어 ${n}개`,
    showLess: '간략히 보기',
    added: '추가됨',
    removed: '삭제됨',
  },
};

/**
 * @param {'unchanged' | 'showLess' | 'added' | 'removed'} key
 * @param {number} [count]
 */
export function historyDiffText(key, count = 0) {
  const value = STRINGS[i18n.locale]?.[key] ?? STRINGS.en[key];
  return typeof value === 'function' ? value(count) : value;
}
