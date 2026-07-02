import antd from 'antd/locale/zh_TW'
import 'dayjs/locale/zh-tw'
import translation from '../zh-TW.json'
import type { LangBundle } from '../index'

const bundle: LangBundle = { translation, antd, dayjs: 'zh-tw' }
export default bundle
