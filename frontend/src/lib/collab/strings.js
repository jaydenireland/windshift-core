import { i18n } from '../stores/i18n.svelte.js';

// Co-editing UI strings, kept here instead of the app's locale catalogs so a
// fork carrying co-editing never edits upstream locale files. Every
// application locale is covered; unknown locales fall back to English.
const STRINGS = {
  en: {
    connecting: 'Connecting…',
    connected: 'Live',
    reconnecting: 'Reconnecting…',
    closed: 'Offline',
    gone: 'This page was archived or deleted. Live editing has stopped.',
    mergeWarning:
      'An outside change could not be merged cleanly into the live page. Check the page history if something looks off.',
    peopleAria: 'Also on this page',
    itemGone: 'This item was deleted or merged. Live editing has stopped.',
    itemMergeWarning:
      'An outside change could not be merged cleanly into the description. Check the item history if something looks off.',
    itemPeopleAria: 'Also editing the description',
    done: 'Done',
    autosaves: 'Changes save automatically',
  },
  de: {
    connecting: 'Verbinden…',
    connected: 'Verbunden',
    reconnecting: 'Erneut verbinden…',
    closed: 'Getrennt',
    gone: 'Diese Seite wurde archiviert oder gelöscht. Die Live-Bearbeitung wurde beendet.',
    mergeWarning:
      'Eine externe Änderung konnte nicht sauber in die geöffnete Seite übernommen werden. Prüfen Sie den Seitenverlauf, falls etwas nicht stimmt.',
    peopleAria: 'Ebenfalls auf dieser Seite',
    itemGone:
      'Dieses Element wurde gelöscht oder zusammengeführt. Die Live-Bearbeitung wurde beendet.',
    itemMergeWarning:
      'Eine externe Änderung konnte nicht sauber in die Beschreibung übernommen werden. Prüfen Sie den Verlauf, falls etwas nicht stimmt.',
    itemPeopleAria: 'Bearbeiten die Beschreibung ebenfalls',
    done: 'Fertig',
    autosaves: 'Änderungen werden automatisch gespeichert',
  },
  fr: {
    connecting: 'Connexion…',
    connected: 'En direct',
    reconnecting: 'Reconnexion…',
    closed: 'Hors ligne',
    gone: 'Cette page a été archivée ou supprimée. La modification en direct est arrêtée.',
    mergeWarning:
      'Une modification externe n’a pas pu être fusionnée proprement dans la page ouverte. Consultez l’historique de la page si quelque chose semble incorrect.',
    peopleAria: 'Également sur cette page',
    itemGone: 'Cet élément a été supprimé ou fusionné. La modification en direct est arrêtée.',
    itemMergeWarning:
      'Une modification externe n’a pas pu être fusionnée proprement dans la description. Consultez l’historique de l’élément si quelque chose semble incorrect.',
    itemPeopleAria: 'Modifient aussi la description',
    done: 'Terminé',
    autosaves: 'Les modifications sont enregistrées automatiquement',
  },
  ru: {
    connecting: 'Подключение…',
    connected: 'Онлайн',
    reconnecting: 'Переподключение…',
    closed: 'Офлайн',
    gone: 'Эта страница была архивирована или удалена. Совместное редактирование остановлено.',
    mergeWarning:
      'Внешнее изменение не удалось аккуратно объединить с открытой страницей. Если что-то выглядит неправильно, проверьте историю страницы.',
    peopleAria: 'Также на этой странице',
    itemGone: 'Этот элемент удалён или объединён. Совместное редактирование остановлено.',
    itemMergeWarning:
      'Внешнее изменение не удалось аккуратно объединить с описанием. Если что-то выглядит неправильно, проверьте историю элемента.',
    itemPeopleAria: 'Также редактируют описание',
    done: 'Готово',
    autosaves: 'Изменения сохраняются автоматически',
  },
  'zh-CN': {
    connecting: '正在连接…',
    connected: '实时',
    reconnecting: '正在重新连接…',
    closed: '离线',
    gone: '此页面已归档或删除，实时编辑已停止。',
    mergeWarning: '外部更改未能完整合并到当前页面。如有异常，请查看页面历史记录。',
    peopleAria: '也在此页面上',
    itemGone: '此事项已被删除或合并，实时编辑已停止。',
    itemMergeWarning: '外部更改未能完整合并到描述中。如有异常，请查看事项历史记录。',
    itemPeopleAria: '也在编辑描述',
    done: '完成',
    autosaves: '更改会自动保存',
  },
  es: {
    connecting: 'Conectando…',
    connected: 'En vivo',
    reconnecting: 'Reconectando…',
    closed: 'Sin conexión',
    gone: 'Esta página se archivó o eliminó. La edición en vivo se detuvo.',
    mergeWarning:
      'Un cambio externo no se pudo combinar limpiamente con la página abierta. Revisa el historial de la página si algo no parece correcto.',
    peopleAria: 'También en esta página',
    itemGone: 'Este elemento se eliminó o se fusionó. La edición en vivo se detuvo.',
    itemMergeWarning:
      'Un cambio externo no se pudo combinar limpiamente con la descripción. Revisa el historial del elemento si algo no parece correcto.',
    itemPeopleAria: 'También editando la descripción',
    done: 'Listo',
    autosaves: 'Los cambios se guardan automáticamente',
  },
  'pt-BR': {
    connecting: 'Conectando…',
    connected: 'Ao vivo',
    reconnecting: 'Reconectando…',
    closed: 'Desconectado',
    gone: 'Esta página foi arquivada ou excluída. A edição ao vivo foi interrompida.',
    mergeWarning:
      'Uma alteração externa não pôde ser mesclada corretamente na página aberta. Verifique o histórico da página se algo parecer errado.',
    peopleAria: 'Também nesta página',
    itemGone: 'Este item foi excluído ou mesclado. A edição ao vivo foi interrompida.',
    itemMergeWarning:
      'Uma alteração externa não pôde ser mesclada corretamente na descrição. Verifique o histórico do item se algo parecer errado.',
    itemPeopleAria: 'Também editando a descrição',
    done: 'Concluído',
    autosaves: 'As alterações são salvas automaticamente',
  },
  ar: {
    connecting: 'جارٍ الاتصال…',
    connected: 'مباشر',
    reconnecting: 'جارٍ إعادة الاتصال…',
    closed: 'غير متصل',
    gone: 'تمت أرشفة هذه الصفحة أو حذفها. توقف التحرير المباشر.',
    mergeWarning:
      'تعذّر دمج تغيير خارجي بشكل سليم في الصفحة المفتوحة. راجع سجل الصفحة إذا بدا شيء غير صحيح.',
    peopleAria: 'موجودون أيضًا في هذه الصفحة',
    itemGone: 'تم حذف هذا العنصر أو دمجه. توقف التحرير المباشر.',
    itemMergeWarning:
      'تعذّر دمج تغيير خارجي بشكل سليم في الوصف. راجع سجل العنصر إذا بدا شيء غير صحيح.',
    itemPeopleAria: 'يحررون الوصف أيضًا',
    done: 'تم',
    autosaves: 'تُحفظ التغييرات تلقائيًا',
  },
  ko: {
    connecting: '연결 중…',
    connected: '실시간',
    reconnecting: '다시 연결 중…',
    closed: '오프라인',
    gone: '이 페이지가 보관되었거나 삭제되었습니다. 실시간 편집이 중지되었습니다.',
    mergeWarning:
      '외부 변경 사항을 열린 페이지에 깔끔하게 병합하지 못했습니다. 이상한 점이 있으면 페이지 기록을 확인하세요.',
    peopleAria: '이 페이지에 함께 있는 사람',
    itemGone: '이 항목이 삭제되었거나 병합되었습니다. 실시간 편집이 중지되었습니다.',
    itemMergeWarning:
      '외부 변경 사항을 설명에 깔끔하게 병합하지 못했습니다. 이상한 점이 있으면 항목 기록을 확인하세요.',
    itemPeopleAria: '설명을 함께 편집 중인 사람',
    done: '완료',
    autosaves: '변경 사항이 자동으로 저장됩니다',
  },
};

/**
 * Translate a co-editing string for the current application locale.
 * @param {keyof typeof STRINGS.en} key
 */
export function collabText(key) {
  return STRINGS[i18n.locale]?.[key] ?? STRINGS.en[key];
}
