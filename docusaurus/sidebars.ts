import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
    docsSidebar: [
        {type: 'html', value: '<span class="menu__link">INTRO</span>', className: 'sidebar-title'},
        {type: 'doc', id: 'intro'},
        {type: 'doc', id: 'get-started'},
        {type: 'html', value: '<span class="menu__link">HOW-TO</span>', className: 'sidebar-title'},
        {type: 'doc', id: 'persistent-shares'},
        {type: 'html', value: '<span class="menu__link">REFERENCE</span>', className: 'sidebar-title'},
        {type: 'doc', id: 'configuration'},
        {type: 'doc', id: 'common-servers'},
    ],
};

export default sidebars;
