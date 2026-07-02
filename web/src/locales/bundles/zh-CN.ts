import antd from 'antd/locale/zh_CN'
import 'dayjs/locale/zh-cn'
import translation from '../zh-CN.json'
import type { LangBundle } from '../index'

const bundle: LangBundle = { translation, antd, dayjs: 'zh-cn' }
export default bundle
